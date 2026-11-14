package snr

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

	snrapi "github.com/medik8s/self-node-remediation/v5/api/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/yaml"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/listener"
)

const (
	otherNamespace = "wao-other"
	// managerUser stands for the WAO ServiceAccount. It has the ClusterRole from config/rbac/role.yaml and nothing more.
	managerUser         = "system:serviceaccount:wao-test:wao-manager"
	multipleTemplates   = "remediation.medik8s.io/multiple-templates-support"
	deleteWarning       = "The default configuration is deleted, Self Node Remediation is now disabled"
	failedCallingPrefix = "failed calling webhook"
)

func repoRoot() string { return filepath.Join("..", "..", "..") }

// warningRecorder keeps the admission warnings that the API server sends to the administrator client.
type warningRecorder struct {
	mu   sync.Mutex
	text []string
}

func (w *warningRecorder) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.text = append(w.text, text)
}

// take returns the warnings since the last call.
func (w *warningRecorder) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.text
	w.text = nil
	return out
}

// admissionSuite is an envtest API server with the registrations of config/webhook routed to one
// WAO manager in this process. No SNR controller and no agent runs here, so a SelfNodeRemediation
// starts no remediation and a SelfNodeRemediationConfig starts no DaemonSet.
type admissionSuite struct {
	ctx      context.Context
	c        client.Client // a cluster administrator; requests go through admission
	warnings *warningRecorder
	stop     context.CancelFunc
	done     chan error
	once     sync.Once
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

// startAdmissionSuite starts the API server with the SNR CRDs and the registrations that
// controller-gen writes from the markers of the copied files. The WAO manager of the suite uses a
// user that has only the ClusterRole that controller-gen writes for the WAO ServiceAccount.
func startAdmissionSuite(t *testing.T) *admissionSuite {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run this suite through make test or make test-envtest")
	}
	keepTaintFlags(t)
	// The WAO Deployment sets this variable from the namespace of the Pod.
	t.Setenv(namespaceEnvVar, testNamespace)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot(), "config", "operands", "snr", "config", "crd", "bases")},
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
	warnings := &warningRecorder{}
	adminCfg := rest.CopyConfig(cfg)
	adminCfg.WarningHandler = warnings
	admin, err := client.New(adminCfg, client.Options{Scheme: snrScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

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
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-test-" + role.Name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: managerUser}},
	}
	if err := admin.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}

	managerCfg := rest.CopyConfig(cfg)
	managerCfg.Impersonate = rest.ImpersonationConfig{UserName: managerUser, Groups: []string{"system:authenticated"}}
	mgr, err := ctrl.NewManager(managerCfg, ctrl.Options{
		Scheme:                 snrScheme(t),
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
	s := &admissionSuite{ctx: ctx, c: admin, warnings: warnings, stop: cancel, done: make(chan error, 1)}
	go func() { s.done <- mgr.Start(runCtx) }()
	t.Cleanup(func() { s.shutdown(t) })
	eventually(t, "the webhook listener serves", listener.Ready(mgr))

	for _, name := range []string{testNamespace, otherNamespace} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	s.warnings.take()
	return s
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

// suiteConfig returns a config without the durations. The API server applies the CRD defaults.
func suiteConfig(name, namespace string) *snrapi.SelfNodeRemediationConfig {
	config := snrapi.NewDefaultSelfNodeRemediationConfig()
	config.Name, config.Namespace = name, namespace
	return &config
}

func suiteTemplate(name string, annotations map[string]string) *snrapi.SelfNodeRemediationTemplate {
	template := testTemplate(snrapi.OutOfServiceTaintRemediationStrategy)
	template.Name = name
	template.Annotations = annotations
	return template
}

// This test sends requests through the API server to the registered handlers.
// The steps share one API server and run in order.
func TestAdmissionThroughAPIServer(t *testing.T) {
	s := startAdmissionSuite(t)
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"the registration has three Fail entries and one Ignore entry", s.registration},
		{"the user of the WAO manager has no write verb on the SNR kinds", s.permissions},
		{"the config rules reject and warn through the API server", s.config},
		{"the template defaulter adds the annotation to the stored object", s.template},
		{"a SelfNodeRemediation is admitted", s.remediation},
		{"without a serving listener, only the SelfNodeRemediation requests pass", s.noListener},
	} {
		if !t.Run(step.name, step.run) {
			t.Fatalf("stop after the failed step %q: the steps share state", step.name)
		}
	}
}

func (s *admissionSuite) registration(t *testing.T) {
	type entry struct {
		policy     admissionregistrationv1.FailurePolicyType
		operations string
		resources  string
	}
	want := map[string]entry{
		"mselfnoderemediationtemplate.kb.io": {admissionregistrationv1.Fail, "[CREATE UPDATE]", "[selfnoderemediationtemplates]"},
		"vselfnoderemediationtemplate.kb.io": {admissionregistrationv1.Fail, "[CREATE UPDATE]", "[selfnoderemediationtemplates]"},
		"vselfnoderemediationconfig.kb.io":   {admissionregistrationv1.Fail, "[CREATE UPDATE DELETE]", "[selfnoderemediationconfigs]"},
		// A remediation must not depend on the admission listener.
		"vselfnoderemediation.kb.io": {admissionregistrationv1.Ignore, "[CREATE UPDATE]", "[selfnoderemediations]"},
	}
	got := map[string]entry{}
	add := func(name string, policy *admissionregistrationv1.FailurePolicyType, rules []admissionregistrationv1.RuleWithOperations) {
		if len(rules) != 1 || !slices.Contains(rules[0].APIGroups, snrapi.GroupVersion.Group) {
			return
		}
		e := entry{operations: fmt.Sprint(rules[0].Operations), resources: fmt.Sprint(rules[0].Resources)}
		if policy != nil {
			e.policy = *policy
		}
		got[name] = e
	}
	validating := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := s.c.List(s.ctx, validating); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range validating.Items {
		for _, w := range cfg.Webhooks {
			add(w.Name, w.FailurePolicy, w.Rules)
		}
	}
	mutating := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	if err := s.c.List(s.ctx, mutating); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range mutating.Items {
		for _, w := range cfg.Webhooks {
			add(w.Name, w.FailurePolicy, w.Rules)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("SNR entries:\n got %v\nwant %v", got, want)
	}
}

// permissions asks the API server what the user of the WAO manager can do. SNR admission reads no
// object, so the user keeps only the read verbs of the WAO reconciler.
func (s *admissionSuite) permissions(t *testing.T) {
	group := snrapi.GroupVersion.Group
	for _, tc := range []struct {
		verb, group, resource, subresource string
		want                               bool
	}{
		{"list", group, "selfnoderemediationconfigs", "", true},
		{"list", group, "selfnoderemediations", "", true},
		{"list", group, "selfnoderemediationtemplates", "", true},
		{"create", group, "selfnoderemediationconfigs", "", false},
		{"update", group, "selfnoderemediationconfigs", "", false},
		{"delete", group, "selfnoderemediationconfigs", "", false},
		{"create", group, "selfnoderemediationtemplates", "", false},
		{"delete", group, "selfnoderemediationtemplates", "", false},
		{"create", group, "selfnoderemediations", "", false},
		{"update", group, "selfnoderemediations", "status", false},
		{"delete", group, "selfnoderemediations", "", false},
		{"create", "apps", "daemonsets", "", false},
	} {
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: managerUser, Groups: []string{"system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: tc.verb, Group: tc.group, Resource: tc.resource,
				Subresource: tc.subresource, Namespace: testNamespace},
		}}
		if err := s.c.Create(s.ctx, review); err != nil {
			t.Fatal(err)
		}
		if review.Status.Allowed != tc.want {
			t.Errorf("%s %s/%s/%s: allowed = %v, want %v", tc.verb, tc.group, tc.resource, tc.subresource,
				review.Status.Allowed, tc.want)
		}
	}
}

func (s *admissionSuite) config(t *testing.T) {
	// The default config of the SNR manager: the default name in the namespace of the WAO installation.
	if err := s.c.Create(s.ctx, suiteConfig(snrapi.ConfigCRName, testNamespace), client.DryRunAll); err != nil {
		t.Fatalf("the default config was rejected: %v", err)
	}
	key := client.ObjectKey{Namespace: testNamespace, Name: snrapi.ConfigCRName}
	if err := s.c.Get(s.ctx, key, &snrapi.SelfNodeRemediationConfig{}); !apierrors.IsNotFound(err) {
		t.Errorf("a dry-run request left an object: %v", err)
	}
	if got := s.warnings.take(); len(got) != 0 {
		t.Errorf("the default config gave the warnings %v", got)
	}

	wantRejected(t, s.c.Create(s.ctx, suiteConfig("other-name", testNamespace), client.DryRunAll),
		"a name other than self-node-remediation-config is not allowed")
	wantRejected(t, s.c.Create(s.ctx, suiteConfig(snrapi.ConfigCRName, otherNamespace), client.DryRunAll),
		"SelfNodeRemediationConfig is only allowed to be created in the namespace: "+testNamespace)

	short := suiteConfig(snrapi.ConfigCRName, testNamespace)
	short.Spec.ApiCheckInterval = &metav1.Duration{Duration: 500 * time.Millisecond}
	wantRejected(t, s.c.Create(s.ctx, short, client.DryRunAll), "ApiCheckInterval cannot be less than 1s")

	toleration := suiteConfig(snrapi.ConfigCRName, testNamespace)
	toleration.Spec.CustomDsTolerations = []corev1.Toleration{{Key: "wao", Operator: corev1.TolerationOpExists, Value: "set"}}
	wantRejected(t, s.c.Create(s.ctx, toleration, client.DryRunAll),
		"invalid value for toleration, value must be empty for Operator value is Exists")

	// A timeout pair that is not safe gives a warning. The request passes.
	unsafe := suiteConfig(snrapi.ConfigCRName, testNamespace)
	unsafe.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 6 * time.Second}
	if err := s.c.Create(s.ctx, unsafe, client.DryRunAll); err != nil {
		t.Errorf("a config with a timeout warning was rejected: %v", err)
	}
	if got := s.warnings.take(); len(got) != 1 || !strings.Contains(got[0], "PeerRequestTimeout (6s) is less than ApiServerTimeout + MinimumBuffer") {
		t.Errorf("warnings = %v, want the timeout warning", got)
	}

	// UPDATE and DELETE need a stored object.
	stored := suiteConfig(snrapi.ConfigCRName, testNamespace)
	if err := s.c.Create(s.ctx, stored); err != nil {
		t.Fatal(err)
	}
	changed := stored.DeepCopy()
	changed.Spec.PeerUpdateInterval = &metav1.Duration{Duration: time.Second}
	wantRejected(t, s.c.Update(s.ctx, changed), "PeerUpdateInterval cannot be less than 10s")
	stored.Spec.PeerUpdateInterval = &metav1.Duration{Duration: 20 * time.Minute}
	if err := s.c.Update(s.ctx, stored); err != nil {
		t.Errorf("a valid update was rejected: %v", err)
	}
	s.warnings.take()
	// A person removes the agents with a delete of the config. No rule rejects it.
	if err := s.c.Delete(s.ctx, stored); err != nil {
		t.Fatalf("the delete of the default config was rejected: %v", err)
	}
	if got := s.warnings.take(); len(got) != 1 || got[0] != deleteWarning {
		t.Errorf("warnings = %v, want %q", got, deleteWarning)
	}
}

func (s *admissionSuite) template(t *testing.T) {
	stored := func(name string) *snrapi.SelfNodeRemediationTemplate {
		t.Helper()
		template := &snrapi.SelfNodeRemediationTemplate{}
		if err := s.c.Get(s.ctx, client.ObjectKey{Namespace: testNamespace, Name: name}, template); err != nil {
			t.Fatal(err)
		}
		return template
	}
	// The API server of this suite is newer than Kubernetes 1.26, so the strategy rule passes.
	if err := s.c.Create(s.ctx, suiteTemplate("wao-plain", nil)); err != nil {
		t.Fatalf("a template with the OutOfServiceTaint strategy was rejected: %v", err)
	}
	plain := stored("wao-plain")
	if plain.Annotations[multipleTemplates] != "true" {
		t.Fatalf("annotations = %v, want %s=true from the defaulter", plain.Annotations, multipleTemplates)
	}

	// The defaulter also runs for an UPDATE. A removal of the annotation does not persist.
	plain.Annotations = nil
	if err := s.c.Update(s.ctx, plain); err != nil {
		t.Fatal(err)
	}
	if got := stored("wao-plain").Annotations[multipleTemplates]; got != "true" {
		t.Errorf("annotation after a removal = %q, want true", got)
	}

	// The defaulter does not replace a value.
	if err := s.c.Create(s.ctx, suiteTemplate("wao-false", map[string]string{multipleTemplates: "false"})); err != nil {
		t.Fatal(err)
	}
	if got := stored("wao-false").Annotations[multipleTemplates]; got != "false" {
		t.Errorf("annotation = %q, want the value false of the request", got)
	}
}

func (s *admissionSuite) remediation(t *testing.T) {
	remediation := testRemediation(snrapi.OutOfServiceTaintRemediationStrategy)
	if err := s.c.Create(s.ctx, remediation); err != nil {
		t.Fatalf("a SelfNodeRemediation was rejected: %v", err)
	}
	remediation.Spec.RemediationStrategy = snrapi.ResourceDeletionRemediationStrategy
	if err := s.c.Update(s.ctx, remediation); err != nil {
		t.Errorf("an update of a SelfNodeRemediation was rejected: %v", err)
	}
}

func (s *admissionSuite) noListener(t *testing.T) {
	config := suiteConfig(snrapi.ConfigCRName, testNamespace)
	if err := s.c.Create(s.ctx, config); err != nil {
		t.Fatal(err)
	}
	template := &snrapi.SelfNodeRemediationTemplate{}
	if err := s.c.Get(s.ctx, client.ObjectKey{Namespace: testNamespace, Name: "wao-plain"}, template); err != nil {
		t.Fatal(err)
	}
	remediation := &snrapi.SelfNodeRemediation{}
	if err := s.c.Get(s.ctx, client.ObjectKey{Namespace: testNamespace, Name: "node-a"}, remediation); err != nil {
		t.Fatal(err)
	}
	s.shutdown(t)

	// The three entries with Fail.
	wantRejected(t, s.c.Create(s.ctx, suiteTemplate("wao-no-backend", nil), client.DryRunAll), failedCallingPrefix)
	changedTemplate := template.DeepCopy()
	changedTemplate.Labels = map[string]string{"wao.test/changed": "true"}
	wantRejected(t, s.c.Update(s.ctx, changedTemplate), failedCallingPrefix)
	changedConfig := config.DeepCopy()
	changedConfig.Spec.PeerUpdateInterval = &metav1.Duration{Duration: 30 * time.Minute}
	wantRejected(t, s.c.Update(s.ctx, changedConfig), failedCallingPrefix)
	// The config entry has DELETE. A delete of the config needs a WAO replica.
	wantRejected(t, s.c.Delete(s.ctx, config), failedCallingPrefix)

	// The entry with Ignore: a remediation does not depend on the listener.
	second := testRemediation(snrapi.AutomaticRemediationStrategy)
	second.Name = "node-b"
	if err := s.c.Create(s.ctx, second); err != nil {
		t.Errorf("the SelfNodeRemediation create without a listener failed: %v", err)
	}
	remediation.Spec.RemediationStrategy = snrapi.AutomaticRemediationStrategy
	if err := s.c.Update(s.ctx, remediation); err != nil {
		t.Errorf("the SelfNodeRemediation update without a listener failed: %v", err)
	}

	// The status subresource has no registration, so it needs no backend.
	now := metav1.Now()
	remediation.Status.LastError = "wao-test"
	remediation.Status.TimeAssumedRebooted = &now
	if err := s.c.Status().Update(s.ctx, remediation); err != nil {
		t.Errorf("the status update without a listener failed: %v", err)
	}
	// The template and remediation entries have no DELETE operation.
	if err := s.c.Delete(s.ctx, template); err != nil {
		t.Errorf("the template delete without a listener failed: %v", err)
	}
	if err := s.c.Delete(s.ctx, remediation); err != nil {
		t.Errorf("the SelfNodeRemediation delete without a listener failed: %v", err)
	}
}
