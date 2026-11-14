package nhc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	nhcapi "github.com/medik8s/node-healthcheck-operator/v5/api/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/yaml"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/listener"
)

const (
	templateNamespace = "nhc-test"
	// managerUser stands for the WAO ServiceAccount. It has the ClusterRole from config/rbac/role.yaml and nothing more.
	managerUser = "system:serviceaccount:nhc-test:wao-manager"
	// multipleTemplates is the annotation that permits two templates of one kind in a NodeHealthCheck.
	multipleTemplates = "remediation.medik8s.io/multiple-templates-support"
	nhcCRDName        = "nodehealthchecks.remediation.medik8s.io"
)

var (
	// bundledTemplateGVK is a template kind in the WAO bundle that has no admission webhook. The
	// registrations of this suite contain the webhooks of the other operands, and this suite serves
	// only the NHC handler. A SelfNodeRemediationTemplate is not usable here: its mutating entry has
	// the failurePolicy Fail, and its defaulter adds the annotation again after a removal.
	bundledTemplateGVK = schema.GroupVersionKind{Group: "machine-deletion-remediation.medik8s.io", Version: "v1alpha1",
		Kind: "MachineDeletionRemediationTemplate"}
	// vendorTemplateGVK is a template kind that is not in the WAO bundle.
	vendorTemplateGVK = schema.GroupVersionKind{Group: "vendor.wao.test", Version: "v1", Kind: "VendorRemediationTemplate"}
)

func repoRoot() string { return filepath.Join("..", "..", "..") }

// admissionSuite is an envtest API server with the registrations of config/webhook routed to one
// WAO manager in this process. No NHC controller runs here, so a NodeHealthCheck starts no remediation.
type admissionSuite struct {
	ctx     context.Context
	cfg     *rest.Config
	c       client.Client // a cluster administrator; requests go through admission
	options envtest.WebhookInstallOptions
	stop    context.CancelFunc
	done    chan error
	once    sync.Once
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func wantRejected(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("error = %v, want a rejection that contains %q", err, message)
	}
}

// vendorTemplateCRD is the CRD of a remediation template from a different vendor.
func vendorTemplateCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "vendorremediationtemplates." + vendorTemplateGVK.Group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: vendorTemplateGVK.Group,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "vendorremediationtemplates",
				Singular: "vendorremediationtemplate", Kind: vendorTemplateGVK.Kind, ListKind: vendorTemplateGVK.Kind + "List"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: vendorTemplateGVK.Version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: ptr.To(true)}}}},
		},
	}
}

// startAdmissionSuite starts the API server with the operand CRDs and the registrations that
// controller-gen writes from the markers of the copied files. The WAO manager of the suite uses a
// user that has only the ClusterRole that controller-gen writes for the WAO ServiceAccount.
func startAdmissionSuite(t *testing.T) *admissionSuite {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run this suite through make test or make test-envtest")
	}
	operandKeys := []string{"nhc", "snr", "sbr", "far", "mdr", "nmo"}
	crdPaths := make([]string, 0, len(operandKeys))
	for _, key := range operandKeys {
		crdPaths = append(crdPaths, filepath.Join(repoRoot(), "config", "operands", key, "config", "crd", "bases"))
	}
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		CRDs:                  []*apiextensionsv1.CustomResourceDefinition{vendorTemplateCRD()},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join(repoRoot(), "config", "webhook", "manifests.yaml")},
		},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	scheme := nhcScheme(t)
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: templateNamespace}}); err != nil {
		t.Fatal(err)
	}

	// The ClusterRole of the WAO ServiceAccount, as controller-gen writes it from the RBAC markers.
	data, err := os.ReadFile(filepath.Join(repoRoot(), "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.UnmarshalStrict(data, role); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	bind(t, admin, role.Name)

	managerCfg := rest.CopyConfig(cfg)
	managerCfg.Impersonate = rest.ImpersonationConfig{UserName: managerUser, Groups: []string{"system:authenticated"}}
	mgr, err := ctrl.NewManager(managerCfg, ctrl.Options{
		Scheme:                 nhcScheme(t),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    testEnv.WebhookInstallOptions.LocalServingHost,
			Port:    testEnv.WebhookInstallOptions.LocalServingPort,
			CertDir: testEnv.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same call as in cmd/main.go.
	if err := Initialize(mgr); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s := &admissionSuite{ctx: ctx, cfg: cfg, c: admin, options: testEnv.WebhookInstallOptions, stop: cancel, done: make(chan error, 1)}
	go func() { s.done <- mgr.Start(runCtx) }()
	t.Cleanup(func() { s.shutdown(t) })
	eventually(t, "the webhook listener serves", listener.Ready(mgr))
	return s
}

// bind gives the user of the WAO manager the rules of a ClusterRole.
func bind(t *testing.T, c client.Client, roleName string) {
	t.Helper()
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-test-" + roleName},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: managerUser}},
	}
	if err := c.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
}

// shutdown stops the WAO manager and waits for it. A second call does nothing.
func (s *admissionSuite) shutdown(t *testing.T) {
	t.Helper()
	s.once.Do(func() {
		s.stop()
		select {
		case err := <-s.done:
			if err != nil {
				t.Errorf("manager stop: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop")
		}
	})
}

// createTemplate creates a remediation template object. This suite serves no webhook of a remediator,
// so the test sets the annotation that the remediator webhook sets on a cluster.
func (s *admissionSuite) createTemplate(t *testing.T, gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	t.Helper()
	template := &unstructured.Unstructured{}
	template.SetGroupVersionKind(gvk)
	template.SetNamespace(templateNamespace)
	template.SetName(name)
	template.SetAnnotations(map[string]string{multipleTemplates: "true"})
	if err := unstructured.SetNestedMap(template.Object, map[string]any{}, "spec", "template", "spec"); err != nil {
		t.Fatal(err)
	}
	if err := s.c.Create(s.ctx, template); err != nil {
		t.Fatal(err)
	}
	return template
}

// twoTemplates returns a NodeHealthCheck with two escalating remediations of one template kind.
func twoTemplates(name string, gvk schema.GroupVersionKind, first, second string) *nhcapi.NodeHealthCheck {
	policy := testNHC(name)
	policy.Spec.RemediationTemplate = nil
	for i, template := range []string{first, second} {
		policy.Spec.EscalatingRemediations = append(policy.Spec.EscalatingRemediations, nhcapi.EscalatingRemediation{
			RemediationTemplate: corev1.ObjectReference{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind,
				Namespace: templateNamespace, Name: template},
			Order: i, Timeout: metav1.Duration{Duration: time.Minute},
		})
	}
	return policy
}

// This test sends requests through the API server to the registered handler.
// The steps share one API server and run in order.
func TestAdmissionThroughAPIServer(t *testing.T) {
	s := startAdmissionSuite(t)
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"the registration is one Fail entry for CREATE, UPDATE and DELETE", s.registration},
		{"CREATE of a valid NodeHealthCheck is admitted", s.createValid},
		{"CREATE with an invalid spec is rejected", s.rejectInvalid},
		{"a duplicate template kind needs the annotation on each template in the cluster", s.duplicateBundledKind},
		{"a duplicate template kind that is not in the WAO bundle needs a permission from an administrator", s.duplicateVendorKind},
		{"UPDATE and DELETE are rejected during a remediation until a status patch", s.remediationStatus},
		{"without a serving listener, CREATE, UPDATE and DELETE fail and a status update passes", s.noListener},
		{"a CRD deletion removes the NodeHealthChecks without a serving listener", s.crdDeletion},
	} {
		if !t.Run(step.name, step.run) {
			t.Fatalf("stop after the failed step %q: the steps share state", step.name)
		}
	}
}

func (s *admissionSuite) registration(t *testing.T) {
	validating := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := s.c.List(s.ctx, validating); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, cfg := range validating.Items {
		for _, w := range cfg.Webhooks {
			if len(w.Rules) == 0 || !slices.Contains(w.Rules[0].APIGroups, nhcapi.GroupVersion.Group) {
				continue
			}
			found++
			if w.Name != "vnodehealthcheck.kb.io" || len(w.Rules) != 1 {
				t.Errorf("entry %s with %d rules, want vnodehealthcheck.kb.io with 1 rule", w.Name, len(w.Rules))
			}
			if w.FailurePolicy == nil || *w.FailurePolicy != admissionregistrationv1.Fail {
				t.Errorf("failurePolicy = %v, want Fail", w.FailurePolicy)
			}
			if got := fmt.Sprint(w.Rules[0].Operations); got != "[CREATE UPDATE DELETE]" {
				t.Errorf("operations = %s, want CREATE, UPDATE and DELETE", got)
			}
			if got := fmt.Sprint(w.Rules[0].Resources); got != "[nodehealthchecks]" {
				t.Errorf("resources = %s, want nodehealthchecks only (no status subresource)", got)
			}
		}
	}
	if found != 1 {
		t.Fatalf("%d NHC entries, want 1", found)
	}
}

func (s *admissionSuite) createValid(t *testing.T) {
	if err := s.c.Create(s.ctx, testNHC("wao-valid"), client.DryRunAll); err != nil {
		t.Fatalf("a valid NodeHealthCheck was rejected: %v", err)
	}
	if err := s.c.Get(s.ctx, client.ObjectKey{Name: "wao-valid"}, &nhcapi.NodeHealthCheck{}); !apierrors.IsNotFound(err) {
		t.Errorf("a dry-run request left an object: %v", err)
	}
}

func (s *admissionSuite) rejectInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		change  func(policy *nhcapi.NodeHealthCheck)
		message string
	}{
		"minHealthy and maxUnhealthy": {func(policy *nhcapi.NodeHealthCheck) {
			policy.Spec.MaxUnhealthy = policy.Spec.MinHealthy
		}, "minHealthy and maxUnhealthy cannot be specified at the same time"},
		"no template": {func(policy *nhcapi.NodeHealthCheck) {
			policy.Spec.RemediationTemplate = nil
		}, "Either RemediationTemplate or at least one EscalatingRemediations must be set"},
		"template and escalating remediations": {func(policy *nhcapi.NodeHealthCheck) {
			policy.Spec.EscalatingRemediations = twoTemplates("x", bundledTemplateGVK, "a", "b").Spec.EscalatingRemediations[:1]
		}, "RemediationTemplate and EscalatingRemediations usage is mutual exclusive"},
		"empty selector": {func(policy *nhcapi.NodeHealthCheck) {
			policy.Spec.Selector = metav1.LabelSelector{}
		}, "Selector is mandatory"},
		"timeout below one minute": {func(policy *nhcapi.NodeHealthCheck) {
			policy.Spec.RemediationTemplate = nil
			policy.Spec.EscalatingRemediations = twoTemplates("x", bundledTemplateGVK, "a", "b").Spec.EscalatingRemediations[:1]
			policy.Spec.EscalatingRemediations[0].Timeout = metav1.Duration{Duration: 30 * time.Second}
		}, "EscalatingRemediation Timeout must be at least one minute"},
	} {
		t.Run(name, func(t *testing.T) {
			policy := testNHC("wao-invalid")
			tc.change(policy)
			wantRejected(t, s.c.Create(s.ctx, policy, client.DryRunAll), tc.message)
		})
	}
}

// The WAO ServiceAccount has list permission on the template kinds in the WAO bundle. The handler
// reads the current templates for each request.
func (s *admissionSuite) duplicateBundledKind(t *testing.T) {
	first := s.createTemplate(t, bundledTemplateGVK, "mdr-a")
	s.createTemplate(t, bundledTemplateGVK, "mdr-b")
	policy := twoTemplates("wao-mdr", bundledTemplateGVK, "mdr-a", "mdr-b")
	if err := s.c.Create(s.ctx, policy, client.DryRunAll); err != nil {
		t.Fatalf("two templates with the annotation were rejected: %v", err)
	}
	first.SetAnnotations(nil)
	if err := s.c.Update(s.ctx, first); err != nil {
		t.Fatal(err)
	}
	wantRejected(t, s.c.Create(s.ctx, policy.DeepCopy(), client.DryRunAll),
		"Using multiple templates of same kind is not supported for this template: duplicate template kind: MachineDeletionRemediationTemplate")
}

// Decision D2 of the NHC webhook plan: a template kind that is not in the WAO bundle has no list
// permission. The answer is then "not supported", and the text does not show the cause.
func (s *admissionSuite) duplicateVendorKind(t *testing.T) {
	s.createTemplate(t, vendorTemplateGVK, "vendor-a")
	s.createTemplate(t, vendorTemplateGVK, "vendor-b")
	policy := twoTemplates("wao-vendor", vendorTemplateGVK, "vendor-a", "vendor-b")
	wantRejected(t, s.c.Create(s.ctx, policy, client.DryRunAll),
		"Using multiple templates of same kind is not supported for this template: duplicate template kind: VendorRemediationTemplate")

	// One template of that kind needs no list, so it is admitted.
	single := testNHC("wao-vendor-single")
	single.Spec.RemediationTemplate = &policy.Spec.EscalatingRemediations[0].RemediationTemplate
	if err := s.c.Create(s.ctx, single, client.DryRunAll); err != nil {
		t.Errorf("one template of a kind that is not in the bundle was rejected: %v", err)
	}

	// The documented procedure: an administrator gives the WAO ServiceAccount list on the template resource.
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "vendor-template-list"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{vendorTemplateGVK.Group},
			Resources: []string{"vendorremediationtemplates"}, Verbs: []string{"list"}}},
	}
	if err := s.c.Create(s.ctx, role); err != nil {
		t.Fatal(err)
	}
	bind(t, s.c, role.Name)
	// The authorizer of the API server reads the new binding after a short time.
	eventually(t, "the duplicate kind is admitted after the administrator adds the permission", func() bool {
		return s.c.Create(s.ctx, policy.DeepCopy(), client.DryRunAll) == nil
	})
}

// Decision D1 of the NHC webhook plan. No NHC controller runs in this suite, as on a cluster with
// NHC Disabled: nothing removes the remediation entry from the status.
func (s *admissionSuite) remediationStatus(t *testing.T) {
	policy := testNHC("wao-remediating")
	if err := s.c.Create(s.ctx, policy); err != nil {
		t.Fatal(err)
	}
	// The registration has no status subresource, so admission does not validate this update.
	policy.Status.UnhealthyNodes = []*nhcapi.UnhealthyNode{{Name: "worker-test", Remediations: []*nhcapi.Remediation{{
		Resource: corev1.ObjectReference{APIVersion: "wao.test/v1", Kind: "SyntheticRemediation", Namespace: templateNamespace, Name: "worker-test"},
		Started:  metav1.Now()}}}}
	if err := s.c.Status().Update(s.ctx, policy); err != nil {
		t.Fatal(err)
	}

	wantRejected(t, s.c.Delete(s.ctx, policy.DeepCopy()), "deletion prohibited due to running remediation")
	wantRejected(t, s.c.Delete(s.ctx, policy.DeepCopy(), client.DryRunAll), "deletion prohibited due to running remediation")

	changed := policy.DeepCopy()
	changed.Spec.Selector.MatchExpressions[0].Key = changedSelectorKey
	wantRejected(t, s.c.Update(s.ctx, changed), "selector update prohibited due to running remediation")
	changed = policy.DeepCopy()
	changed.Spec.RemediationTemplate.Name = "template-changed"
	wantRejected(t, s.c.Update(s.ctx, changed), "remediation template update prohibited due to running remediation")

	// A field that is not restricted can change.
	paused := policy.DeepCopy()
	paused.Spec.PauseRequests = []string{"wao-test"}
	if err := s.c.Update(s.ctx, paused); err != nil {
		t.Fatalf("a pause request during a remediation was rejected: %v", err)
	}

	// The documented procedure: remove the entry with a patch to the status subresource.
	patch := client.RawPatch("application/merge-patch+json", []byte(`{"status":{"unhealthyNodes":null}}`))
	if err := s.c.Status().Patch(s.ctx, paused, patch); err != nil {
		t.Fatalf("the status patch failed: %v", err)
	}
	current := &nhcapi.NodeHealthCheck{}
	if err := s.c.Get(s.ctx, client.ObjectKeyFromObject(policy), current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.UnhealthyNodes) != 0 {
		t.Fatalf("the status patch left %d unhealthy nodes", len(current.Status.UnhealthyNodes))
	}
	current.Spec.Selector.MatchExpressions[0].Key = changedSelectorKey
	if err := s.c.Update(s.ctx, current); err != nil {
		t.Errorf("the selector change after the status patch was rejected: %v", err)
	}
	if err := s.c.Delete(s.ctx, current); err != nil {
		t.Errorf("the delete after the status patch was rejected: %v", err)
	}
}

// NHC has a DELETE entry with failurePolicy Fail, which FAR does not have.
func (s *admissionSuite) noListener(t *testing.T) {
	kept := testNHC("wao-kept")
	if err := s.c.Create(s.ctx, kept); err != nil {
		t.Fatal(err)
	}
	s.shutdown(t)

	wantRejected(t, s.c.Create(s.ctx, testNHC("wao-no-backend"), client.DryRunAll), "failed calling webhook")
	changed := kept.DeepCopy()
	changed.Spec.PauseRequests = []string{"wao-test"}
	wantRejected(t, s.c.Update(s.ctx, changed), "failed calling webhook")
	wantRejected(t, s.c.Delete(s.ctx, kept.DeepCopy()), "failed calling webhook")

	// The status subresource has no registration, so it needs no backend.
	kept.Status.UnhealthyNodes = []*nhcapi.UnhealthyNode{{Name: "worker-test", Remediations: []*nhcapi.Remediation{{
		Resource: corev1.ObjectReference{APIVersion: "wao.test/v1", Kind: "SyntheticRemediation", Namespace: templateNamespace, Name: "worker-test"},
		Started:  metav1.Now()}}}}
	if err := s.c.Status().Update(s.ctx, kept); err != nil {
		t.Errorf("the status update without a listener failed: %v", err)
	}
}

// Risk R4 of the NHC webhook plan. The object wao-kept exists, its status shows a remediation, and
// no listener serves. A direct delete fails in this state. The CRD deletion must still complete:
// the API server removes the objects of a deleted CRD without a call to admission webhooks.
func (s *admissionSuite) crdDeletion(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: nhcCRDName}}
	if err := s.c.Delete(s.ctx, crd); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the NodeHealthCheck CRD is gone", func() bool {
		return apierrors.IsNotFound(s.c.Get(s.ctx, client.ObjectKeyFromObject(crd), &apiextensionsv1.CustomResourceDefinition{}))
	})
}
