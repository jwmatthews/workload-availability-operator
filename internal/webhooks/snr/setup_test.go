package snr

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	snrapi "github.com/medik8s/self-node-remediation/v5/api/v1alpha1"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/snr/utils"
)

const (
	namespaceEnvVar = "OPERATOR_NAMESPACE"
	testNamespace   = "wao-test"

	configPath           = "/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig"
	templateMutatePath   = "/mutate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate"
	templateValidatePath = "/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate"
	remediationPath      = "/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation"
)

var snrPaths = []string{configPath, templateMutatePath, templateValidatePath, remediationPath}

func snrScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, snrapi.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// stubAPIServer answers only the version request that SNR admission makes at start.
// versionStatus is the HTTP status of that request. 0 means 200.
func stubAPIServer(t *testing.T, minor string, versionStatus int) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		if versionStatus != 0 {
			http.Error(w, "synthetic version failure", versionStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(version.Info{Major: "1", Minor: minor})
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

// keepTaintFlags restores the package variables of the copied taints.go after the test.
func keepTaintFlags(t *testing.T) {
	t.Helper()
	supported, ga := utils.IsOutOfServiceTaintSupported, utils.IsOutOfServiceTaintGA
	t.Cleanup(func() { utils.IsOutOfServiceTaintSupported, utils.IsOutOfServiceTaintGA = supported, ga })
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

func wantNoneRegistered(t *testing.T, mgr ctrl.Manager) {
	t.Helper()
	for _, path := range snrPaths {
		if registered(mgr, path) {
			t.Errorf("the handler %s is registered after an error", path)
		}
	}
}

// review sends an AdmissionReview to a handler that the manager registered.
func review(t *testing.T, mgr ctrl.Manager, path, kind string, op admissionv1.Operation, obj, old any) *admissionv1.AdmissionResponse {
	t.Helper()
	request := &admissionv1.AdmissionRequest{
		UID:       "wao-test",
		Kind:      metav1.GroupVersionKind{Group: snrapi.GroupVersion.Group, Version: snrapi.GroupVersion.Version, Kind: kind},
		Operation: op,
	}
	for target, source := range map[*runtime.RawExtension]any{&request.Object: obj, &request.OldObject: old} {
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
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
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

func wantDenied(t *testing.T, resp *admissionv1.AdmissionResponse, message string) {
	t.Helper()
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, message) {
		t.Errorf("response = allowed %v, result %+v, want a rejection with %q", resp.Allowed, resp.Result, message)
	}
}

func testConfig(name, namespace string) *snrapi.SelfNodeRemediationConfig {
	config := snrapi.NewDefaultSelfNodeRemediationConfig()
	config.TypeMeta = metav1.TypeMeta{APIVersion: snrapi.GroupVersion.String(), Kind: "SelfNodeRemediationConfig"}
	config.Name, config.Namespace = name, namespace
	// The API server applies these CRD defaults before it calls the validator. The test has no API server.
	for _, field := range []**metav1.Duration{&config.Spec.PeerApiServerTimeout, &config.Spec.ApiServerTimeout,
		&config.Spec.PeerDialTimeout, &config.Spec.PeerRequestTimeout, &config.Spec.ApiCheckInterval, &config.Spec.PeerUpdateInterval} {
		if *field == nil {
			*field = &metav1.Duration{Duration: 15 * 60 * 1e9}
		}
	}
	return &config
}

func testTemplate(strategy snrapi.RemediationStrategyType) *snrapi.SelfNodeRemediationTemplate {
	template := &snrapi.SelfNodeRemediationTemplate{
		TypeMeta:   metav1.TypeMeta{APIVersion: snrapi.GroupVersion.String(), Kind: "SelfNodeRemediationTemplate"},
		ObjectMeta: metav1.ObjectMeta{Name: "template", Namespace: testNamespace},
	}
	template.Spec.Template.Spec.RemediationStrategy = strategy
	return template
}

func testRemediation(strategy snrapi.RemediationStrategyType) *snrapi.SelfNodeRemediation {
	return &snrapi.SelfNodeRemediation{
		TypeMeta:   metav1.TypeMeta{APIVersion: snrapi.GroupVersion.String(), Kind: "SelfNodeRemediation"},
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", Namespace: testNamespace},
		Spec:       snrapi.SelfNodeRemediationSpec{RemediationStrategy: strategy},
	}
}

func TestSetupRejectsNilManager(t *testing.T) {
	if err := SetupWithManager(nil); err == nil {
		t.Error("SetupWithManager accepted a nil manager")
	}
	if err := Initialize(nil); err == nil {
		t.Error("Initialize accepted a nil manager")
	}
}

func TestSetupWithManagerReturnsRegistrationError(t *testing.T) {
	// The SNR types are not in this scheme, so the webhook builder cannot find their kind.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, stubAPIServer(t, "29", 0), scheme)
	err := SetupWithManager(mgr)
	if err == nil || !strings.Contains(err.Error(), "register the SelfNodeRemediationConfig webhook") {
		t.Fatalf("error = %v, want the registration error", err)
	}
}

// Without the namespace, the config validator rejects each create and the delete of the default
// config. The start must stop instead (decision S2).
func TestInitializeFailsWithoutTheNamespace(t *testing.T) {
	keepTaintFlags(t)
	t.Setenv(namespaceEnvVar, testNamespace)
	if err := os.Unsetenv(namespaceEnvVar); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, stubAPIServer(t, "29", 0), snrScheme(t))
	err := Initialize(mgr)
	if err == nil || !strings.Contains(err.Error(), namespaceEnvVar+" must be set") {
		t.Fatalf("error = %v, want the missing namespace error", err)
	}
	wantNoneRegistered(t, mgr)
}

// A failed read of the Kubernetes version must stop the start (decision S3). The SNR manager
// continues and rejects the OutOfServiceTaint strategy. The copied function tries for 10 seconds.
func TestInitializeFailsWhenTheVersionReadFails(t *testing.T) {
	if testing.Short() {
		t.Skip("the copied retry needs 10 seconds")
	}
	keepTaintFlags(t)
	t.Setenv(namespaceEnvVar, testNamespace)
	mgr := newTestManager(t, stubAPIServer(t, "29", http.StatusInternalServerError), snrScheme(t))
	err := Initialize(mgr)
	if err == nil || !strings.Contains(err.Error(), "read the Kubernetes version") {
		t.Fatalf("error = %v, want the version read error", err)
	}
	wantNoneRegistered(t, mgr)
}

func TestInitializeRegistersTheHandlers(t *testing.T) {
	keepTaintFlags(t)
	t.Setenv(namespaceEnvVar, testNamespace)
	utils.IsOutOfServiceTaintSupported = false
	mgr := newTestManager(t, stubAPIServer(t, "29+", 0), snrScheme(t))
	if err := Initialize(mgr); err != nil {
		t.Fatal(err)
	}
	for _, path := range snrPaths {
		if !registered(mgr, path) {
			t.Errorf("the handler %s is not registered", path)
		}
	}
	if !utils.IsOutOfServiceTaintSupported {
		t.Error("Kubernetes 1.29 must support the OutOfServiceTaint strategy")
	}
}

// The strategy rule uses the version that Initialize read.
func TestStrategyRuleUsesTheVersionOfTheCluster(t *testing.T) {
	for minor, wantAllowed := range map[string]bool{"25": false, "26": true} {
		t.Run("1."+minor, func(t *testing.T) {
			keepTaintFlags(t)
			t.Setenv(namespaceEnvVar, testNamespace)
			mgr := newTestManager(t, stubAPIServer(t, minor, 0), snrScheme(t))
			if err := Initialize(mgr); err != nil {
				t.Fatal(err)
			}
			for path, resp := range map[string]*admissionv1.AdmissionResponse{
				templateValidatePath: review(t, mgr, templateValidatePath, "SelfNodeRemediationTemplate", admissionv1.Create,
					testTemplate(snrapi.OutOfServiceTaintRemediationStrategy), nil),
				remediationPath: review(t, mgr, remediationPath, "SelfNodeRemediation", admissionv1.Create,
					testRemediation(snrapi.OutOfServiceTaintRemediationStrategy), nil),
			} {
				if resp.Allowed != wantAllowed {
					t.Errorf("%s: allowed = %v, want %v (%+v)", path, resp.Allowed, wantAllowed, resp.Result)
				}
			}
		})
	}
}

// Each registered handler must answer the operations of its marker.
func TestHandlersAnswerEachOperation(t *testing.T) {
	keepTaintFlags(t)
	t.Setenv(namespaceEnvVar, testNamespace)
	mgr := newTestManager(t, stubAPIServer(t, "29", 0), snrScheme(t))
	if err := Initialize(mgr); err != nil {
		t.Fatal(err)
	}
	const configKind = "SelfNodeRemediationConfig"
	valid := testConfig(snrapi.ConfigCRName, testNamespace)

	if resp := review(t, mgr, configPath, configKind, admissionv1.Create, valid, nil); !resp.Allowed {
		t.Errorf("the default config in the WAO namespace was rejected: %+v", resp.Result)
	}
	wantDenied(t, review(t, mgr, configPath, configKind, admissionv1.Create, testConfig("other", testNamespace), nil),
		"a name other than "+snrapi.ConfigCRName+" is not allowed")
	wantDenied(t, review(t, mgr, configPath, configKind, admissionv1.Create, testConfig(snrapi.ConfigCRName, "other"), nil),
		"only allowed to be created in the namespace: "+testNamespace)

	short := valid.DeepCopy()
	short.Spec.ApiCheckInterval = &metav1.Duration{Duration: 1e6}
	wantDenied(t, review(t, mgr, configPath, configKind, admissionv1.Update, short, valid), "ApiCheckInterval cannot be less than 1s")

	resp := review(t, mgr, configPath, configKind, admissionv1.Delete, nil, valid)
	if !resp.Allowed || len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "The default configuration is deleted") {
		t.Errorf("delete of the default config: allowed %v, warnings %v, want a pass with the warning", resp.Allowed, resp.Warnings)
	}

	resp = review(t, mgr, templateMutatePath, "SelfNodeRemediationTemplate", admissionv1.Create,
		testTemplate(snrapi.AutomaticRemediationStrategy), nil)
	if !resp.Allowed || !strings.Contains(string(resp.Patch), "multiple-templates-support") {
		t.Errorf("template defaulter: allowed %v, patch %s, want the annotation", resp.Allowed, resp.Patch)
	}
	old := testRemediation(snrapi.AutomaticRemediationStrategy)
	if resp := review(t, mgr, remediationPath, "SelfNodeRemediation", admissionv1.Update,
		testRemediation(snrapi.ResourceDeletionRemediationStrategy), old); !resp.Allowed {
		t.Errorf("an update of a SelfNodeRemediation was rejected: %+v", resp.Result)
	}
}
