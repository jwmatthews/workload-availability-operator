package far

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"
	"github.com/medik8s/fence-agents-remediation/v5/pkg/validation"

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

const (
	testNamespace = "far-test"
	testAgent     = "fence_wao_listed"
)

var farPaths = []string{
	"/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation",
	"/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation",
	"/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate",
	"/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate",
}

func farScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, farapi.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// versionServer is a stub API server that answers only the version discovery request.
func versionServer(t *testing.T, minor string, status int) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" || status != http.StatusOK {
			http.Error(w, "synthetic failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"major": "1", "minor": minor})
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}
}

// newTestManager returns a manager that is never started. Its own client has no reachable API server.
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

// restoreFARState puts the FAR package globals back to the FAR defaults after a test.
func restoreFARState(t *testing.T) {
	t.Helper()
	previous := farapi.IsOutOfServiceTaintSupported
	t.Cleanup(func() {
		farapi.SetAgentValidator(validation.NewAgentValidator())
		farapi.SetOutOfServiceTaintSupported(previous)
	})
}

type secretReads struct{ keys []client.ObjectKey }

func spyAdmissionClient(t *testing.T, reads *secretReads, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(farScheme(t)).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isSecret := obj.(*corev1.Secret); isSecret {
				reads.keys = append(reads.keys, key)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
}

func listedOnly(agent string) (bool, error) { return agent == testAgent, nil }

func testFAR() *farapi.FenceAgentsRemediation {
	return &farapi.FenceAgentsRemediation{
		TypeMeta:   metav1.TypeMeta{APIVersion: farapi.GroupVersion.String(), Kind: "FenceAgentsRemediation"},
		ObjectMeta: metav1.ObjectMeta{Name: "wao-admission-test", Namespace: testNamespace},
		Spec: farapi.FenceAgentsRemediationSpec{
			Agent:               testAgent,
			RemediationStrategy: farapi.ResourceDeletionRemediationStrategy,
			NodeParameters: map[farapi.ParameterName]map[farapi.NodeName]string{
				"--ipport": {"worker-test": "6230"},
			},
		},
	}
}

// review sends a CREATE AdmissionReview for obj to the handler registered at path.
func review(t *testing.T, mgr ctrl.Manager, path, kind string, obj any) *admissionv1.AdmissionResponse {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       "wao-test",
			Kind:      metav1.GroupVersionKind{Group: farapi.GroupVersion.Group, Version: farapi.GroupVersion.Version, Kind: kind},
			Operation: admissionv1.Create,
			Namespace: testNamespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mgr.GetWebhookServer().WebhookMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", path, rec.Code, rec.Body.String())
	}
	var out admissionv1.AdmissionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Response == nil {
		t.Fatalf("%s: no response in %s", path, rec.Body.String())
	}
	return out.Response
}

func TestSetupWithManagerRejectsMissingOptions(t *testing.T) {
	restoreFARState(t)
	mgr := newTestManager(t, versionServer(t, "29", http.StatusOK), farScheme(t))
	admissionClient := spyAdmissionClient(t, &secretReads{})
	for name, opts := range map[string]Options{
		"no agent checker":    {AdmissionClient: admissionClient},
		"no admission client": {AgentExists: listedOnly},
		"no options":          {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := SetupWithManager(mgr, opts); err == nil {
				t.Fatal("SetupWithManager accepted incomplete options")
			}
		})
	}
	if err := SetupWithManager(nil, Options{AgentExists: listedOnly, AdmissionClient: admissionClient}); err == nil {
		t.Fatal("SetupWithManager accepted a nil manager")
	}
}

func TestSetupWithManagerReturnsRegistrationError(t *testing.T) {
	restoreFARState(t)
	// The FAR types are not in this scheme, so the webhook builder cannot find their kinds.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, versionServer(t, "29", http.StatusOK), scheme)
	err := SetupWithManager(mgr, Options{AgentExists: listedOnly, AdmissionClient: spyAdmissionClient(t, &secretReads{})})
	if err == nil {
		t.Fatal("SetupWithManager did not return the registration error")
	}
}

func TestSetupWithManagerRegistersFourHandlersWithAdmissionClient(t *testing.T) {
	restoreFARState(t)
	mgr := newTestManager(t, versionServer(t, "29", http.StatusOK), farScheme(t))
	reads := &secretReads{}
	legacy := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: farapi.OldDefaultSecretName, Namespace: testNamespace},
		Data: map[string][]byte{"--username": []byte("synthetic-user")}}
	opts := Options{AgentExists: listedOnly, AdmissionClient: spyAdmissionClient(t, reads, legacy), OutOfServiceTaintSupported: true}
	if err := SetupWithManager(mgr, opts); err != nil {
		t.Fatal(err)
	}
	if !farapi.IsOutOfServiceTaintSupported {
		t.Error("the strategy support flag was not set")
	}

	tmpl := &farapi.FenceAgentsRemediationTemplate{
		TypeMeta:   metav1.TypeMeta{APIVersion: farapi.GroupVersion.String(), Kind: "FenceAgentsRemediationTemplate"},
		ObjectMeta: metav1.ObjectMeta{Name: "wao-admission-test", Namespace: testNamespace},
		Spec:       farapi.FenceAgentsRemediationTemplateSpec{Template: farapi.FenceAgentsRemediationTemplateResource{Spec: testFAR().Spec}},
	}
	requests := map[string]struct {
		kind string
		obj  any
	}{
		farPaths[0]: {"FenceAgentsRemediation", testFAR()},
		farPaths[1]: {"FenceAgentsRemediation", testFAR()},
		farPaths[2]: {"FenceAgentsRemediationTemplate", tmpl},
		farPaths[3]: {"FenceAgentsRemediationTemplate", tmpl},
	}
	for _, path := range farPaths {
		resp := review(t, mgr, path, requests[path].kind, requests[path].obj)
		if !resp.Allowed {
			t.Errorf("%s: request denied: %v", path, resp.Result)
		}
		if strings.HasPrefix(path, "/mutate-") {
			// The legacy Secret exists only in the admission client. The manager client has no API server.
			if !strings.Contains(string(resp.Patch), farapi.OldDefaultSecretName) {
				t.Errorf("%s: the defaulter did not read the legacy Secret through the admission client; patch %s", path, resp.Patch)
			}
		}
	}
	if len(reads.keys) == 0 {
		t.Error("no Secret read used the admission client")
	}

	// The registered validators use the agent checker from the options.
	unlisted := testFAR()
	unlisted.Spec.Agent = "fence_ipmilan"
	resp := review(t, mgr, farPaths[1], "FenceAgentsRemediation", unlisted)
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, "unsupported fence agent: fence_ipmilan") {
		t.Errorf("an agent that the checker does not list was not rejected: %+v", resp.Result)
	}
}

func TestSetupWithManagerSetsUnsupportedStrategyState(t *testing.T) {
	restoreFARState(t)
	mgr := newTestManager(t, versionServer(t, "25", http.StatusOK), farScheme(t))
	opts := Options{AgentExists: listedOnly, AdmissionClient: spyAdmissionClient(t, &secretReads{})}
	if err := SetupWithManager(mgr, opts); err != nil {
		t.Fatal(err)
	}
	far := testFAR()
	far.Spec.RemediationStrategy = farapi.OutOfServiceTaintRemediationStrategy
	resp := review(t, mgr, farPaths[1], "FenceAgentsRemediation", far)
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, "OutOfServiceTaint remediation strategy is not supported") {
		t.Errorf("OutOfServiceTaint was not rejected: %+v", resp.Result)
	}
}

func writeInventory(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func registered(mgr ctrl.Manager, path string) bool {
	// The server creates its mux at the first registration.
	mux := mgr.GetWebhookServer().WebhookMux()
	if mux == nil {
		return false
	}
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, path, nil))
	return pattern == path
}

func TestInitializeStartupErrors(t *testing.T) {
	valid := testAgent + "\n"
	for name, tc := range map[string]struct {
		inventory func(t *testing.T) string
		cfg       func(t *testing.T) *rest.Config
		noFAR     bool
		wantErr   string
	}{
		"missing inventory": {
			inventory: func(t *testing.T) string { return filepath.Join(t.TempDir(), "agents.txt") },
			wantErr:   "FAR agent inventory"},
		"empty inventory":   {inventory: func(t *testing.T) string { return writeInventory(t, "") }, wantErr: "FAR agent inventory"},
		"invalid inventory": {inventory: func(t *testing.T) string { return writeInventory(t, "not-an-agent\n") }, wantErr: "FAR agent inventory"},
		"version discovery fails": {
			cfg:     func(t *testing.T) *rest.Config { return versionServer(t, "29", http.StatusInternalServerError) },
			wantErr: "Kubernetes version"},
		"API server is not reachable": {
			cfg:     func(t *testing.T) *rest.Config { return &rest.Config{Host: "127.0.0.1:1"} },
			wantErr: "Kubernetes version"},
		"registration fails": {noFAR: true, wantErr: "register"},
	} {
		t.Run(name, func(t *testing.T) {
			restoreFARState(t)
			cfg := versionServer(t, "29", http.StatusOK)
			if tc.cfg != nil {
				cfg = tc.cfg(t)
			}
			scheme := farScheme(t)
			if tc.noFAR {
				scheme = runtime.NewScheme()
				if err := clientgoscheme.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
			}
			inventoryPath := writeInventory(t, valid)
			if tc.inventory != nil {
				inventoryPath = tc.inventory(t)
			}
			mgr := newTestManager(t, cfg, scheme)
			err := Initialize(mgr, inventoryPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			for _, path := range farPaths {
				if registered(mgr, path) && !tc.noFAR {
					t.Errorf("%s is registered after a startup error", path)
				}
			}
		})
	}
}

func TestInitializeRegistersHandlersFromInventoryAndDiscovery(t *testing.T) {
	for minor, wantSupported := range map[string]bool{"25": false, "26": true, "29+": true} {
		t.Run("Kubernetes 1."+minor, func(t *testing.T) {
			restoreFARState(t)
			mgr := newTestManager(t, versionServer(t, minor, http.StatusOK), farScheme(t))
			if err := Initialize(mgr, writeInventory(t, testAgent+"\n")); err != nil {
				t.Fatal(err)
			}
			for _, path := range farPaths {
				if !registered(mgr, path) {
					t.Errorf("%s is not registered", path)
				}
			}
			if farapi.IsOutOfServiceTaintSupported != wantSupported {
				t.Errorf("OutOfServiceTaint support = %v, want %v", farapi.IsOutOfServiceTaintSupported, wantSupported)
			}
			// The validator uses the inventory: the listed name passes the agent check, another name does not.
			unlisted := testFAR()
			unlisted.Spec.Agent = "fence_ipmilan"
			resp := review(t, mgr, farPaths[1], "FenceAgentsRemediation", unlisted)
			if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, "unsupported fence agent") {
				t.Errorf("an unlisted agent was not rejected: %+v", resp.Result)
			}
		})
	}
}
