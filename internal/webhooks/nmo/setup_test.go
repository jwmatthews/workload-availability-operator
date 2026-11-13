package nmo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	nmoapi "github.com/medik8s/node-maintenance-operator/v5/api/v1beta1"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const nmoPath = "/validate-nodemaintenance-medik8s-io-v1beta1-nodemaintenance"

func nmoScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, nmoapi.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// stubAPIServer answers only the group discovery that the OpenShift check makes.
// discoveryStatus is the HTTP status of the group discovery. 0 means 200.
func stubAPIServer(t *testing.T, openShift bool, discoveryStatus int) *rest.Config {
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
			if discoveryStatus != 0 {
				http.Error(w, "synthetic discovery failure", discoveryStatus)
				return
			}
			groups := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
			if openShift {
				gv := metav1.GroupVersionForDiscovery{GroupVersion: "config.openshift.io/v1", Version: "v1"}
				groups.Groups = append(groups.Groups, metav1.APIGroup{Name: "config.openshift.io",
					Versions: []metav1.GroupVersionForDiscovery{gv}, PreferredVersion: gv})
			}
			write(w, groups)
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

func testNM(name, nodeName string) *nmoapi.NodeMaintenance {
	return &nmoapi.NodeMaintenance{
		TypeMeta:   metav1.TypeMeta{APIVersion: nmoapi.GroupVersion.String(), Kind: "NodeMaintenance"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       nmoapi.NodeMaintenanceSpec{NodeName: nodeName, Reason: "wao-test"},
	}
}

func testNode(name string, controlPlane bool) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if controlPlane {
		node.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
	}
	return node
}

// review sends an AdmissionReview to the handler that the manager registered.
func review(t *testing.T, mgr ctrl.Manager, op admissionv1.Operation, obj, old *nmoapi.NodeMaintenance) *admissionv1.AdmissionResponse {
	t.Helper()
	request := &admissionv1.AdmissionRequest{
		UID:       "wao-test",
		Kind:      metav1.GroupVersionKind{Group: nmoapi.GroupVersion.Group, Version: nmoapi.GroupVersion.Version, Kind: "NodeMaintenance"},
		Operation: op,
	}
	for target, source := range map[*runtime.RawExtension]*nmoapi.NodeMaintenance{&request.Object: obj, &request.OldObject: old} {
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
	req := httptest.NewRequest(http.MethodPost, nmoPath, bytes.NewReader(body))
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
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, nmoPath, nil))
	return pattern == nmoPath
}

func wantDenied(t *testing.T, resp *admissionv1.AdmissionResponse, message string) {
	t.Helper()
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, message) {
		t.Errorf("response = allowed %v, result %+v, want a rejection with %q", resp.Allowed, resp.Result, message)
	}
}

func TestSetupWithManagerRejectsMissingOptions(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, false, 0), nmoScheme(t))
	for name, tc := range map[string]struct {
		mgr  ctrl.Manager
		opts Options
	}{
		"nil manager": {nil, Options{AdmissionClient: fake.NewClientBuilder().Build()}},
		"nil client":  {mgr, Options{IsOpenShift: true}},
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
	// The NMO types are not in this scheme, so the webhook builder cannot find their kind.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, stubAPIServer(t, false, 0), scheme)
	err := SetupWithManager(mgr, Options{AdmissionClient: fake.NewClientBuilder().Build()})
	if err == nil || !strings.Contains(err.Error(), "register") {
		t.Fatalf("error = %v, want the registration error", err)
	}
}

// A discovery error must stop the start. An assumed "not OpenShift" answer disables the etcd quorum rule.
func TestInitializeFailsWhenDiscoveryFails(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, true, http.StatusInternalServerError), nmoScheme(t))
	err := Initialize(mgr)
	if err == nil || !strings.Contains(err.Error(), "OpenShift") {
		t.Fatalf("error = %v, want the OpenShift check error", err)
	}
	if registered(mgr) {
		t.Error("the handler is registered after a discovery error")
	}
}

func TestOpenShiftCheckReadsTheDiscoveryAnswer(t *testing.T) {
	for _, want := range []bool{true, false} {
		got, err := isOpenShift(stubAPIServer(t, want, 0))
		if err != nil || got != want {
			t.Errorf("isOpenShift = %v, %v, want %v", got, err, want)
		}
	}
}

func TestInitializeRegistersTheHandler(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, true, 0), nmoScheme(t))
	if err := Initialize(mgr); err != nil {
		t.Fatal(err)
	}
	if !registered(mgr) {
		t.Fatal("the handler is not registered")
	}
}

// The validator must read with the dedicated client, not with the manager client.
// The manager in this test has no reachable API server for these reads.
func TestValidatorReadsWithTheAdmissionClient(t *testing.T) {
	var gets, lists atomic.Int32
	admissionClient := fake.NewClientBuilder().WithScheme(nmoScheme(t)).
		WithObjects(testNode("worker-a", false), testNode("worker-b", false), testNM("existing", "worker-b")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				gets.Add(1)
				return c.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				lists.Add(1)
				return c.List(ctx, list, opts...)
			},
		}).Build()
	mgr := newTestManager(t, stubAPIServer(t, false, 0), nmoScheme(t))
	if err := SetupWithManager(mgr, Options{AdmissionClient: admissionClient}); err != nil {
		t.Fatal(err)
	}

	if resp := review(t, mgr, admissionv1.Create, testNM("valid", "worker-a"), nil); !resp.Allowed {
		t.Errorf("a valid NodeMaintenance was rejected: %+v", resp.Result)
	}
	if gets.Load() != 1 || lists.Load() != 1 {
		t.Errorf("%d gets and %d lists for a worker node, want 1 and 1", gets.Load(), lists.Load())
	}
	wantDenied(t, review(t, mgr, admissionv1.Create, testNM("absent", "no-such-node"), nil),
		"invalid nodeName, no node with name no-such-node found")
	wantDenied(t, review(t, mgr, admissionv1.Create, testNM("second", "worker-b"), nil),
		"a NodeMaintenance for node worker-b already exists")
}

// The marker has CREATE and UPDATE. The handler must answer each operation.
func TestHandlerAnswersUpdate(t *testing.T) {
	mgr := newTestManager(t, stubAPIServer(t, false, 0), nmoScheme(t))
	// The update rule reads no object, so the client has none.
	if err := SetupWithManager(mgr, Options{AdmissionClient: fake.NewClientBuilder().Build(), IsOpenShift: true}); err != nil {
		t.Fatal(err)
	}
	old := testNM("maintenance", "worker-a")
	wantDenied(t, review(t, mgr, admissionv1.Update, testNM("maintenance", "worker-b"), old),
		"updating spec.NodeName isn't allowed")
	changed := old.DeepCopy()
	changed.Spec.Reason = "changed"
	if resp := review(t, mgr, admissionv1.Update, changed, old); !resp.Allowed {
		t.Errorf("a change of the reason was rejected: %+v", resp.Result)
	}
}
