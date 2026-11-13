package v1alpha1

// WAO file, not copied from NHC. These tests record the NHC admission behavior
// that WAO must preserve. They call the copied validator with a mock client,
// so they need no API server. See docs/nhc-webhooks.md.

import (
	"context"
	"strings"
	"testing"
	"time"

	remediationv1alpha1 "github.com/medik8s/node-healthcheck-operator/v5/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/nhc/cluster"
)

const waoMultipleTemplatesAnnotation = "remediation.medik8s.io/multiple-templates-support"

// waoListSpy is the client of the validator. It records each template list.
type waoListSpy struct {
	client.Client
	calls   int
	kinds   []schema.GroupVersionKind
	options [][]client.ListOption
	// items is the list result. err, when set, replaces it.
	items []unstructured.Unstructured
	err   error
}

func (s *waoListSpy) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	s.calls++
	s.kinds = append(s.kinds, list.GetObjectKind().GroupVersionKind())
	s.options = append(s.options, opts)
	if s.err != nil {
		return s.err
	}
	list.(*unstructured.UnstructuredList).Items = s.items
	return nil
}

func waoValidator(spy *waoListSpy, supportedTopology bool) *customValidator {
	return &customValidator{spy, &cluster.Capabilities{IsSupportedControlPlaneTopology: supportedTopology}}
}

func waoTemplateRef(kind, name string) corev1.ObjectReference {
	return corev1.ObjectReference{APIVersion: "wao.test/v1", Kind: kind, Namespace: "wao-test", Name: name}
}

// waoNHC returns a NodeHealthCheck that the validator accepts.
func waoNHC() *remediationv1alpha1.NodeHealthCheck {
	minHealthy := intstr.FromString("51%")
	ref := waoTemplateRef("SyntheticTemplate", "template-a")
	return &remediationv1alpha1.NodeHealthCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-policy"},
		Spec: remediationv1alpha1.NodeHealthCheckSpec{
			MinHealthy: &minHealthy,
			Selector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "node-role.kubernetes.io/control-plane", Operator: metav1.LabelSelectorOpDoesNotExist}}},
			RemediationTemplate: &ref,
		},
	}
}

func waoEscalating(nhc *remediationv1alpha1.NodeHealthCheck, kinds ...string) *remediationv1alpha1.NodeHealthCheck {
	nhc.Spec.RemediationTemplate = nil
	nhc.Spec.EscalatingRemediations = nil
	for i, kind := range kinds {
		nhc.Spec.EscalatingRemediations = append(nhc.Spec.EscalatingRemediations, remediationv1alpha1.EscalatingRemediation{
			RemediationTemplate: waoTemplateRef(kind, "template-"+string(rune('a'+i))),
			Order:               i,
			Timeout:             metav1.Duration{Duration: time.Minute},
		})
	}
	return nhc
}

// waoRemediating gives the NodeHealthCheck a status that shows one remediation.
func waoRemediating(nhc *remediationv1alpha1.NodeHealthCheck) *remediationv1alpha1.NodeHealthCheck {
	nhc.Status.UnhealthyNodes = []*remediationv1alpha1.UnhealthyNode{{Name: "worker-test",
		Remediations: []*remediationv1alpha1.Remediation{{Resource: waoTemplateRef("SyntheticRemediation", "worker-test")}}}}
	return nhc
}

func waoTemplate(name string, annotations map[string]string) unstructured.Unstructured {
	item := unstructured.Unstructured{}
	item.SetName(name)
	item.SetAnnotations(annotations)
	return item
}

func waoWantError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Errorf("error = %v, want it to contain %q", err, text)
	}
}

// The validator lists templates only when two escalating remediations have the same kind.
func TestWAOTemplateListOnlyForADuplicateKind(t *testing.T) {
	for name, nhc := range map[string]*remediationv1alpha1.NodeHealthCheck{
		"one template":    waoNHC(),
		"different kinds": waoEscalating(waoNHC(), "KindA", "KindB"),
	} {
		t.Run(name, func(t *testing.T) {
			spy := &waoListSpy{}
			if _, err := waoValidator(spy, true).ValidateCreate(context.Background(), nhc); err != nil {
				t.Errorf("a valid NodeHealthCheck was rejected: %v", err)
			}
			if spy.calls != 0 {
				t.Errorf("%d template lists, want 0", spy.calls)
			}
		})
	}
}

// The list has the group, version and kind of the template reference and no namespace:
// it reads the templates of that kind in all namespaces.
func TestWAOTemplateListIsClusterWide(t *testing.T) {
	annotated := map[string]string{waoMultipleTemplatesAnnotation: "true"}
	spy := &waoListSpy{items: []unstructured.Unstructured{waoTemplate("template-a", annotated), waoTemplate("template-b", annotated)}}
	nhc := waoEscalating(waoNHC(), "KindA", "KindA")
	if _, err := waoValidator(spy, true).ValidateCreate(context.Background(), nhc); err != nil {
		t.Fatalf("a duplicate kind with the annotation on each template was rejected: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("%d template lists, want 1", spy.calls)
	}
	want := schema.GroupVersionKind{Group: "wao.test", Version: "v1", Kind: "KindA"}
	if spy.kinds[0] != want {
		t.Errorf("list kind = %v, want %v", spy.kinds[0], want)
	}
	if len(spy.options[0]) != 0 {
		t.Errorf("list options = %v, want none (all namespaces)", spy.options[0])
	}
}

// The answer for a duplicate kind comes from the templates in the cluster. A failed list gives
// the answer "not supported", also when the cause is a missing permission (decision D2).
func TestWAODuplicateKindNeedsTheAnnotationOnEachTemplate(t *testing.T) {
	annotated := map[string]string{waoMultipleTemplatesAnnotation: "true"}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "wao.test", Resource: "kindas"}, "", nil)
	for name, tc := range map[string]struct {
		spy     *waoListSpy
		allowed bool
	}{
		"each template has the annotation": {
			&waoListSpy{items: []unstructured.Unstructured{waoTemplate("a", annotated), waoTemplate("b", annotated)}}, true},
		// The check reads the presence of the annotation, not its value.
		"the annotation value is false": {
			&waoListSpy{items: []unstructured.Unstructured{waoTemplate("a", map[string]string{waoMultipleTemplatesAnnotation: "false"})}}, true},
		"one template has no annotation": {
			&waoListSpy{items: []unstructured.Unstructured{waoTemplate("a", annotated), waoTemplate("b", nil)}}, false},
		"no template of the kind exists": {&waoListSpy{}, false},
		"the list is forbidden":          {&waoListSpy{err: forbidden}, false},
	} {
		t.Run(name, func(t *testing.T) {
			nhc := waoEscalating(waoNHC(), "KindA", "KindA")
			_, err := waoValidator(tc.spy, true).ValidateCreate(context.Background(), nhc)
			if tc.allowed && err != nil {
				t.Errorf("rejected: %v", err)
			}
			if !tc.allowed {
				waoWantError(t, err, uniqueRemediatorError+": duplicate template kind: KindA")
			}
		})
	}
}

// An unsupported control plane topology rejects each CREATE and UPDATE. DELETE has no topology check.
func TestWAOUnsupportedTopology(t *testing.T) {
	validator := waoValidator(&waoListSpy{}, false)
	_, err := validator.ValidateCreate(context.Background(), waoNHC())
	waoWantError(t, err, unsupportedCpTopologyError)
	_, err = validator.ValidateUpdate(context.Background(), waoNHC(), waoNHC())
	waoWantError(t, err, unsupportedCpTopologyError)
	if _, err := validator.ValidateDelete(context.Background(), waoNHC()); err != nil {
		t.Errorf("the delete was rejected: %v", err)
	}
}

// While the status shows a remediation, a change of the selector, the template or the escalating
// remediations is rejected. Other changes pass. WAO keeps this rule also when NHC is Disabled
// (decision D1): no controller removes the status entry then.
func TestWAOUpdateDuringRemediation(t *testing.T) {
	for name, tc := range map[string]struct {
		change  func(nhc *remediationv1alpha1.NodeHealthCheck)
		wantErr string
	}{
		"selector": {func(nhc *remediationv1alpha1.NodeHealthCheck) {
			nhc.Spec.Selector.MatchExpressions[0].Key = "wao.test/changed"
		}, "selector update " + OngoingRemediationError},
		"remediation template": {func(nhc *remediationv1alpha1.NodeHealthCheck) {
			nhc.Spec.RemediationTemplate.Name = "template-changed"
		}, "remediation template update " + OngoingRemediationError},
		"escalating remediations": {func(nhc *remediationv1alpha1.NodeHealthCheck) {
			waoEscalating(nhc, "KindA", "KindB")
		}, OngoingRemediationError},
		"pause requests": {func(nhc *remediationv1alpha1.NodeHealthCheck) {
			nhc.Spec.PauseRequests = []string{"wao-test"}
		}, ""},
		"minHealthy": {func(nhc *remediationv1alpha1.NodeHealthCheck) {
			minHealthy := intstr.FromInt32(1)
			nhc.Spec.MinHealthy = &minHealthy
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			old := waoRemediating(waoNHC())
			changed := old.DeepCopy()
			tc.change(changed)
			_, err := waoValidator(&waoListSpy{}, true).ValidateUpdate(context.Background(), old, changed)
			if tc.wantErr == "" && err != nil {
				t.Errorf("rejected: %v", err)
			}
			if tc.wantErr != "" {
				waoWantError(t, err, tc.wantErr)
			}
		})
	}
}

// With no remediation in the status, each of the restricted fields can change.
func TestWAOUpdateWithoutRemediation(t *testing.T) {
	old := waoNHC()
	// An unhealthy node without a remediation entry is not a remediation.
	old.Status.UnhealthyNodes = []*remediationv1alpha1.UnhealthyNode{{Name: "worker-test"}}
	changed := old.DeepCopy()
	changed.Spec.Selector.MatchExpressions[0].Key = "wao.test/changed"
	changed.Spec.RemediationTemplate.Name = "template-changed"
	if _, err := waoValidator(&waoListSpy{}, true).ValidateUpdate(context.Background(), old, changed); err != nil {
		t.Errorf("rejected: %v", err)
	}
}

// The delete rule reads only the status of the object.
func TestWAODeleteDuringRemediation(t *testing.T) {
	validator := waoValidator(&waoListSpy{}, true)
	_, err := validator.ValidateDelete(context.Background(), waoRemediating(waoNHC()))
	waoWantError(t, err, "deletion "+OngoingRemediationError)

	idle := waoNHC()
	idle.Status.UnhealthyNodes = []*remediationv1alpha1.UnhealthyNode{{Name: "worker-test"}}
	if _, err := validator.ValidateDelete(context.Background(), idle); err != nil {
		t.Errorf("the delete of a NodeHealthCheck with no remediation was rejected: %v", err)
	}
}
