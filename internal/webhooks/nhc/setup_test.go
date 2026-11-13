package nhc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nhcapi "github.com/medik8s/node-healthcheck-operator/v5/api/v1alpha1"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/medik8s/workload-availability-operator/internal/components"
	"github.com/medik8s/workload-availability-operator/internal/webhooks/nhc/cluster"
)

const nhcPath = "/validate-remediation-medik8s-io-v1alpha1-nodehealthcheck"

func nhcScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, nhcapi.AddToScheme, configv1.Install} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// stubCluster describes the answers of a stub API server for the capabilities discovery.
type stubCluster struct {
	// openShift adds the config.openshift.io group to the discovery answer.
	openShift bool
	// topology is the control plane topology of the Infrastructure object.
	topology configv1.TopologyMode
	// infrastructureStatus is the HTTP status of the Infrastructure read. 0 means 200.
	infrastructureStatus int
	// discoveryStatus is the HTTP status of the group discovery. 0 means 200.
	discoveryStatus int
}

// stubAPIServer answers only the requests that cluster.NewCapabilities makes.
func stubAPIServer(t *testing.T, c stubCluster) *rest.Config {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			write(w, metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"}})
		case "/apis":
			if c.discoveryStatus != 0 {
				http.Error(w, "synthetic discovery failure", c.discoveryStatus)
				return
			}
			groups := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
			if c.openShift {
				gv := metav1.GroupVersionForDiscovery{GroupVersion: "config.openshift.io/v1", Version: "v1"}
				groups.Groups = append(groups.Groups, metav1.APIGroup{Name: "config.openshift.io",
					Versions: []metav1.GroupVersionForDiscovery{gv}, PreferredVersion: gv})
			}
			write(w, groups)
		case "/apis/config.openshift.io/v1":
			write(w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
				GroupVersion: "config.openshift.io/v1",
				APIResources: []metav1.APIResource{{Name: "infrastructures", SingularName: "infrastructure",
					Kind: "Infrastructure", Verbs: metav1.Verbs{"get", "list", "watch"}}}})
		case "/apis/config.openshift.io/v1/infrastructures/cluster":
			if c.infrastructureStatus != 0 {
				http.Error(w, "synthetic Infrastructure failure", c.infrastructureStatus)
				return
			}
			write(w, configv1.Infrastructure{
				TypeMeta:   metav1.TypeMeta{APIVersion: "config.openshift.io/v1", Kind: "Infrastructure"},
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status:     configv1.InfrastructureStatus{ControlPlaneTopology: c.topology}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}
}

// newTestManager returns a manager that is never started.
func newTestManager(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) ctrl.Manager {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		WebhookServer:          webhook.NewServer(webhook.Options{CertDir: t.TempDir()}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

// testNHC returns a NodeHealthCheck that the validator accepts.
func testNHC(name string) *nhcapi.NodeHealthCheck {
	minHealthy := intstr.FromString("51%")
	return &nhcapi.NodeHealthCheck{
		TypeMeta:   metav1.TypeMeta{APIVersion: nhcapi.GroupVersion.String(), Kind: "NodeHealthCheck"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: nhcapi.NodeHealthCheckSpec{
			MinHealthy: &minHealthy,
			Selector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "node-role.kubernetes.io/control-plane", Operator: metav1.LabelSelectorOpDoesNotExist}}},
			RemediationTemplate: testTemplate("template-a"),
		},
	}
}

// testTemplate refers to a synthetic template kind. No such object exists.
func testTemplate(name string) *corev1.ObjectReference {
	return &corev1.ObjectReference{APIVersion: "wao.test/v1", Kind: "SyntheticRemediationTemplate",
		Namespace: "wao-test", Name: name}
}

// withDuplicateKind gives the NodeHealthCheck two escalating remediations of one template kind.
func withDuplicateKind(nhc *nhcapi.NodeHealthCheck) *nhcapi.NodeHealthCheck {
	nhc.Spec.RemediationTemplate = nil
	nhc.Spec.EscalatingRemediations = []nhcapi.EscalatingRemediation{
		{RemediationTemplate: *testTemplate("template-a"), Order: 0, Timeout: metav1.Duration{Duration: time.Minute}},
		{RemediationTemplate: *testTemplate("template-b"), Order: 1, Timeout: metav1.Duration{Duration: time.Minute}},
	}
	return nhc
}

// review sends an AdmissionReview to the handler that the manager registered.
func review(t *testing.T, mgr ctrl.Manager, op admissionv1.Operation, obj, old *nhcapi.NodeHealthCheck) *admissionv1.AdmissionResponse {
	t.Helper()
	request := &admissionv1.AdmissionRequest{
		UID:       "wao-test",
		Kind:      metav1.GroupVersionKind{Group: nhcapi.GroupVersion.Group, Version: nhcapi.GroupVersion.Version, Kind: "NodeHealthCheck"},
		Operation: op,
	}
	for target, source := range map[*runtime.RawExtension]*nhcapi.NodeHealthCheck{&request.Object: obj, &request.OldObject: old} {
		if source == nil {
			continue
		}
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		target.Raw = raw
	}
	body, err := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, nhcPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mgr.GetWebhookServer().WebhookMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var out admissionv1.AdmissionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Response == nil {
		t.Fatalf("no response in %s", rec.Body.String())
	}
	return out.Response
}

func registered(mgr ctrl.Manager) bool {
	// The server creates its mux at the first registration.
	mux := mgr.GetWebhookServer().WebhookMux()
	if mux == nil {
		return false
	}
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, nhcPath, nil))
	return pattern == nhcPath
}

func wantDenied(t *testing.T, resp *admissionv1.AdmissionResponse, message string) {
	t.Helper()
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, message) {
		t.Errorf("response = allowed %v, result %+v, want a rejection with %q", resp.Allowed, resp.Result, message)
	}
}

func supported() *cluster.Capabilities {
	return &cluster.Capabilities{IsSupportedControlPlaneTopology: true}
}

func TestSetupWithManagerRejectsMissingOptions(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, stubCluster{}), nhcScheme(t))
	admissionClient := fake.NewClientBuilder().Build()
	for name, tc := range map[string]struct {
		mgr  ctrl.Manager
		opts Options
	}{
		"nil manager":      {nil, Options{AdmissionClient: admissionClient, Capabilities: supported()}},
		"nil client":       {mgr, Options{Capabilities: supported()}},
		"nil capabilities": {mgr, Options{AdmissionClient: admissionClient}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := SetupWithManager(tc.mgr, tc.opts); err == nil {
				t.Fatal("SetupWithManager accepted the missing option")
			}
			if registered(mgr) {
				t.Error("the handler is registered after an error")
			}
		})
	}
	if err := Initialize(nil); err == nil {
		t.Error("Initialize accepted a nil manager")
	}
}

func TestSetupWithManagerReturnsRegistrationError(t *testing.T) {
	// The NHC types are not in this scheme, so the webhook builder cannot find their kind.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, stubAPIServer(t, stubCluster{}), scheme)
	err := SetupWithManager(mgr, Options{AdmissionClient: fake.NewClientBuilder().Build(), Capabilities: supported()})
	if err == nil || !strings.Contains(err.Error(), "register") {
		t.Fatalf("error = %v, want the registration error", err)
	}
}

// The validator must list templates with the dedicated client, not with the manager client.
// The manager in this test has no reachable API server for a template list.
func TestValidatorListsTemplatesWithTheAdmissionClient(t *testing.T) {
	var lists atomic.Int32
	annotated := true
	admissionClient := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
			lists.Add(1)
			templates := list.(*unstructured.UnstructuredList)
			for _, name := range []string{"template-a", "template-b"} {
				item := unstructured.Unstructured{}
				item.SetName(name)
				if annotated {
					item.SetAnnotations(map[string]string{"remediation.medik8s.io/multiple-templates-support": "true"})
				}
				templates.Items = append(templates.Items, item)
			}
			return nil
		},
	}).Build()
	mgr := newTestManager(t, stubAPIServer(t, stubCluster{}), nhcScheme(t))
	if err := SetupWithManager(mgr, Options{AdmissionClient: admissionClient, Capabilities: supported()}); err != nil {
		t.Fatal(err)
	}
	if !registered(mgr) {
		t.Fatal("the handler is not registered")
	}

	if resp := review(t, mgr, admissionv1.Create, testNHC("one-template"), nil); !resp.Allowed {
		t.Errorf("a valid NodeHealthCheck was rejected: %+v", resp.Result)
	}
	if lists.Load() != 0 {
		t.Errorf("%d template lists for a NodeHealthCheck with one template, want 0", lists.Load())
	}

	if resp := review(t, mgr, admissionv1.Create, withDuplicateKind(testNHC("duplicate-kind")), nil); !resp.Allowed {
		t.Errorf("a duplicate kind with the annotation on each template was rejected: %+v", resp.Result)
	}
	if lists.Load() != 1 {
		t.Errorf("%d template lists for a duplicate kind, want 1", lists.Load())
	}

	annotated = false
	wantDenied(t, review(t, mgr, admissionv1.Create, withDuplicateKind(testNHC("duplicate-kind")), nil),
		"Using multiple templates of same kind is not supported")
}

// The marker has CREATE, UPDATE and DELETE. The handler must answer each operation.
func TestHandlerAnswersUpdateAndDelete(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, stubCluster{}), nhcScheme(t))
	opts := Options{AdmissionClient: fake.NewClientBuilder().Build(), Capabilities: supported()}
	if err := SetupWithManager(mgr, opts); err != nil {
		t.Fatal(err)
	}
	idle := testNHC("policy")
	if resp := review(t, mgr, admissionv1.Delete, nil, idle); !resp.Allowed {
		t.Errorf("the delete of a NodeHealthCheck with no remediation was rejected: %+v", resp.Result)
	}
	remediating := testNHC("policy")
	remediating.Status.UnhealthyNodes = []*nhcapi.UnhealthyNode{{Name: "worker-test",
		Remediations: []*nhcapi.Remediation{{Resource: *testTemplate("request")}}}}
	wantDenied(t, review(t, mgr, admissionv1.Delete, nil, remediating), "deletion prohibited due to running remediation")

	changed := remediating.DeepCopy()
	changed.Spec.Selector.MatchExpressions[0].Key = "wao.test/changed"
	wantDenied(t, review(t, mgr, admissionv1.Update, changed, remediating), "selector update prohibited due to running remediation")
	// A change of a field that is not restricted passes during a remediation.
	paused := remediating.DeepCopy()
	paused.Spec.PauseRequests = []string{"wao-test"}
	if resp := review(t, mgr, admissionv1.Update, paused, remediating); !resp.Allowed {
		t.Errorf("a pause request during a remediation was rejected: %+v", resp.Result)
	}
}

// The reconciler reports the NodeHealthChecks that admission blocks while NHC is Disabled. Its
// condition (components.NHC().AdmissionBlock) must select the same objects as the delete rule of the handler.
func TestAdmissionBlockOfTheComponentAgreesWithTheHandler(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, stubCluster{}), nhcScheme(t))
	opts := Options{AdmissionClient: fake.NewClientBuilder().Build(), Capabilities: supported()}
	if err := SetupWithManager(mgr, opts); err != nil {
		t.Fatal(err)
	}
	remediation := &nhcapi.Remediation{Resource: *testTemplate("request")}
	for name, nodes := range map[string][]*nhcapi.UnhealthyNode{
		"no unhealthy node":           nil,
		"unhealthy node, no entry":    {{Name: "worker-a"}},
		"one remediation":             {{Name: "worker-a", Remediations: []*nhcapi.Remediation{remediation}}},
		"second node has the entry":   {{Name: "worker-a"}, {Name: "worker-b", Remediations: []*nhcapi.Remediation{remediation}}},
		"two remediations on a node":  {{Name: "worker-a", Remediations: []*nhcapi.Remediation{remediation, remediation}}},
		"unhealthy node, empty slice": {{Name: "worker-a", Remediations: []*nhcapi.Remediation{}}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := testNHC("policy")
			policy.Status.UnhealthyNodes = nodes
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(policy)
			if err != nil {
				t.Fatal(err)
			}
			reported := components.NHC().AdmissionBlock.Blocked(object)
			rejected := !review(t, mgr, admissionv1.Delete, nil, policy).Allowed
			if reported != rejected {
				t.Errorf("the component reports blocked = %v, the handler rejects the delete = %v", reported, rejected)
			}
		})
	}
}

func TestInitializeReadsTheTopology(t *testing.T) {
	for name, tc := range map[string]struct {
		cluster   stubCluster
		supported bool
	}{
		"not OpenShift":             {stubCluster{}, true},
		"OpenShift HighlyAvailable": {stubCluster{openShift: true, topology: configv1.HighlyAvailableTopologyMode}, true},
		"OpenShift SingleReplica":   {stubCluster{openShift: true, topology: configv1.SingleReplicaTopologyMode}, true},
		"OpenShift unknown mode":    {stubCluster{openShift: true, topology: "WaoSyntheticMode"}, false},
		"OpenShift empty mode":      {stubCluster{openShift: true}, false},
	} {
		t.Run(name, func(t *testing.T) {
			mgr := newTestManager(t, stubAPIServer(t, tc.cluster), nhcScheme(t))
			if err := Initialize(mgr); err != nil {
				t.Fatal(err)
			}
			if !registered(mgr) {
				t.Fatal("the handler is not registered")
			}
			resp := review(t, mgr, admissionv1.Create, testNHC("topology"), nil)
			if tc.supported && !resp.Allowed {
				t.Errorf("a valid NodeHealthCheck was rejected: %+v", resp.Result)
			}
			if !tc.supported {
				wantDenied(t, resp, "Unsupported control plane topology")
			}
		})
	}
}

func TestInitializeStartupErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     func(t *testing.T) *rest.Config
		noNHC   bool
		wantErr string
	}{
		"discovery fails": {
			cfg: func(t *testing.T) *rest.Config {
				return stubAPIServer(t, stubCluster{discoveryStatus: http.StatusInternalServerError})
			},
			wantErr: "cluster capabilities"},
		"API server is not reachable": {
			cfg:     func(*testing.T) *rest.Config { return &rest.Config{Host: "127.0.0.1:1"} },
			wantErr: "cluster capabilities"},
		"Infrastructure read fails": {
			cfg: func(t *testing.T) *rest.Config {
				return stubAPIServer(t, stubCluster{openShift: true, infrastructureStatus: http.StatusInternalServerError})
			},
			wantErr: "cluster capabilities"},
		"Infrastructure read is forbidden": {
			cfg: func(t *testing.T) *rest.Config {
				return stubAPIServer(t, stubCluster{openShift: true, infrastructureStatus: http.StatusForbidden})
			},
			wantErr: "cluster capabilities"},
		"Infrastructure is absent": {
			cfg: func(t *testing.T) *rest.Config {
				return stubAPIServer(t, stubCluster{openShift: true, infrastructureStatus: http.StatusNotFound})
			},
			wantErr: "cluster capabilities"},
		"registration fails": {
			cfg:     func(t *testing.T) *rest.Config { return stubAPIServer(t, stubCluster{}) },
			noNHC:   true,
			wantErr: "register"},
	} {
		t.Run(name, func(t *testing.T) {
			scheme := nhcScheme(t)
			if tc.noNHC {
				scheme = runtime.NewScheme()
				if err := clientgoscheme.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
			}
			mgr := newTestManager(t, tc.cfg(t), scheme)
			err := Initialize(mgr)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if registered(mgr) {
				t.Error("the handler is registered after a startup error")
			}
		})
	}
}
