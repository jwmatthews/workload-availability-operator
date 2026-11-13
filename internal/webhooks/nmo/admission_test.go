package nmo

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

	nmoapi "github.com/medik8s/node-maintenance-operator/v5/api/v1beta1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	etcdNamespace = "openshift-etcd"
	// managerUser stands for the WAO ServiceAccount. It has the ClusterRole from config/rbac/role.yaml and nothing more.
	managerUser = "system:serviceaccount:nmo-test:wao-manager"
	quorumText  = "will violate etcd quorum"
)

func repoRoot() string { return filepath.Join("..", "..", "..") }

// admissionSuite is an envtest API server with the registrations of config/webhook routed to one
// WAO manager in this process. No NMO controller runs here, so a NodeMaintenance drains no node.
type admissionSuite struct {
	ctx  context.Context
	c    client.Client // a cluster administrator; requests go through admission
	stop context.CancelFunc
	done chan error
	once sync.Once
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

// clusterVersionCRD puts the group version config.openshift.io/v1 into the API discovery.
// The OpenShift check of NMO reads only that group version.
func clusterVersionCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "clusterversions.config.openshift.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "config.openshift.io",
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "clusterversions",
				Singular: "clusterversion", Kind: "ClusterVersion", ListKind: "ClusterVersionList"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: ptr.To(true)}}}},
		},
	}
}

// startAdmissionSuite starts the API server with the NMO CRD and the registrations that
// controller-gen writes from the markers of the copied files. The WAO manager of the suite uses a
// user that has only the ClusterRole that controller-gen writes for the WAO ServiceAccount.
// The API server has the OpenShift config group, so the etcd quorum rule is active.
func startAdmissionSuite(t *testing.T) *admissionSuite {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run this suite through make test or make test-envtest")
	}
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot(), "config", "operands", "nmo", "config", "crd", "bases")},
		CRDs:                  []*apiextensionsv1.CustomResourceDefinition{clusterVersionCRD()},
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
	admin, err := client.New(cfg, client.Options{Scheme: nmoScheme(t)})
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
		Scheme:                 nmoScheme(t),
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
	s := &admissionSuite{ctx: ctx, c: admin, stop: cancel, done: make(chan error, 1)}
	go func() { s.done <- mgr.Start(runCtx) }()
	t.Cleanup(func() { s.shutdown(t) })
	eventually(t, "the webhook listener serves", listener.Ready(mgr))

	for _, object := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: etcdNamespace}},
		testNode("worker-a", false), testNode("worker-b", false), testNode("control-plane-a", true),
	} {
		if err := admin.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
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

// This test sends requests through the API server to the registered handler.
// The steps share one API server and run in order.
func TestAdmissionThroughAPIServer(t *testing.T) {
	s := startAdmissionSuite(t)
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"the registration is one Fail entry for CREATE and UPDATE", s.registration},
		{"the user of the WAO manager has the read verbs of decision N1 and no write verb", s.permissions},
		{"CREATE of a valid NodeMaintenance is admitted", s.createValid},
		{"CREATE for an absent node or a node in maintenance is rejected", s.rejectInvalid},
		{"the etcd quorum rule reads the etcd objects with the generated ClusterRole", s.quorumRule},
		{"UPDATE of the node name is rejected", s.update},
		{"without a serving listener, CREATE and UPDATE fail, and DELETE and a status update pass", s.noListener},
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
			if len(w.Rules) == 0 || !slices.Contains(w.Rules[0].APIGroups, nmoapi.GroupVersion.Group) {
				continue
			}
			found++
			if w.Name != "vnodemaintenance.kb.io" || len(w.Rules) != 1 {
				t.Errorf("entry %s with %d rules, want vnodemaintenance.kb.io with 1 rule", w.Name, len(w.Rules))
			}
			if w.FailurePolicy == nil || *w.FailurePolicy != admissionregistrationv1.Fail {
				t.Errorf("failurePolicy = %v, want Fail", w.FailurePolicy)
			}
			if got := fmt.Sprint(w.Rules[0].Operations); got != "[CREATE UPDATE]" {
				t.Errorf("operations = %s, want CREATE and UPDATE", got)
			}
			if got := fmt.Sprint(w.Rules[0].Resources); got != "[nodemaintenances]" {
				t.Errorf("resources = %s, want nodemaintenances only (no status subresource)", got)
			}
		}
	}
	if found != 1 {
		t.Fatalf("%d NMO entries, want 1", found)
	}
}

// permissions asks the API server what the user of the WAO manager can do. Admission must not get a
// verb that cordons or drains a node, and it must not read a Secret of the etcd namespace by a list.
func (s *admissionSuite) permissions(t *testing.T) {
	for _, tc := range []struct {
		verb, group, resource, subresource, namespace string
		want                                          bool
	}{
		{"get", "", "nodes", "", "", true},
		{"list", "", "pods", "", etcdNamespace, true},
		{"list", "policy", "poddisruptionbudgets", "", etcdNamespace, true},
		{"list", nmoapi.GroupVersion.Group, "nodemaintenances", "", "", true},
		{"list", "", "nodes", "", "", false},
		{"patch", "", "nodes", "", "", false},
		{"update", "", "nodes", "", "", false},
		{"get", "", "pods", "", etcdNamespace, false},
		{"watch", "", "pods", "", "", false},
		{"delete", "", "pods", "", etcdNamespace, false},
		{"create", "", "pods", "eviction", etcdNamespace, false},
		{"get", "policy", "poddisruptionbudgets", "", etcdNamespace, false},
		{"list", "", "secrets", "", etcdNamespace, false},
		{"create", nmoapi.GroupVersion.Group, "nodemaintenances", "", "", false},
		{"delete", nmoapi.GroupVersion.Group, "nodemaintenances", "", "", false},
	} {
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: managerUser, Groups: []string{"system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: tc.verb, Group: tc.group, Resource: tc.resource,
				Subresource: tc.subresource, Namespace: tc.namespace},
		}}
		if err := s.c.Create(s.ctx, review); err != nil {
			t.Fatal(err)
		}
		if review.Status.Allowed != tc.want {
			t.Errorf("%s %s/%s/%s in namespace %q: allowed = %v, want %v", tc.verb, tc.group, tc.resource,
				tc.subresource, tc.namespace, review.Status.Allowed, tc.want)
		}
	}
}

func (s *admissionSuite) createValid(t *testing.T) {
	if err := s.c.Create(s.ctx, testNM("wao-valid", "worker-a"), client.DryRunAll); err != nil {
		t.Fatalf("a valid NodeMaintenance was rejected: %v", err)
	}
	if err := s.c.Get(s.ctx, client.ObjectKey{Name: "wao-valid"}, &nmoapi.NodeMaintenance{}); !apierrors.IsNotFound(err) {
		t.Errorf("a dry-run request left an object: %v", err)
	}
}

func (s *admissionSuite) rejectInvalid(t *testing.T) {
	wantRejected(t, s.c.Create(s.ctx, testNM("wao-absent", "no-such-node"), client.DryRunAll),
		"invalid nodeName, no node with name no-such-node found")

	if err := s.c.Create(s.ctx, testNM("wao-first", "worker-b")); err != nil {
		t.Fatal(err)
	}
	// The handler reads from the API server, so it sees the first object with no cache delay.
	wantRejected(t, s.c.Create(s.ctx, testNM("wao-second", "worker-b"), client.DryRunAll),
		"invalid nodeName, a NodeMaintenance for node worker-b already exists")
}

// quorumRule checks each answer of the etcd quorum rule. A missing permission of the WAO manager
// gives an error text with "forbidden", so the expected texts also prove the two list permissions.
func (s *admissionSuite) quorumRule(t *testing.T) {
	create := func() error { return s.c.Create(s.ctx, testNM("wao-quorum", "control-plane-a"), client.DryRunAll) }
	wantRejected(t, create(), quorumText) // no PodDisruptionBudget

	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd-guard-pdb", Namespace: etcdNamespace},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guard"}}},
	}
	if err := s.c.Create(s.ctx, pdb); err != nil {
		t.Fatal(err)
	}
	setAllowed := func(allowed int32) {
		t.Helper()
		pdb.Status.DisruptionsAllowed = allowed
		if err := s.c.Status().Update(s.ctx, pdb); err != nil {
			t.Fatal(err)
		}
	}
	setAllowed(1)
	if err := create(); err != nil {
		t.Errorf("a control plane node was rejected with one allowed disruption: %v", err)
	}

	guard := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "guard-control-plane-a", Namespace: etcdNamespace, Labels: map[string]string{"app": "guard"}},
		Spec: corev1.PodSpec{NodeName: "control-plane-a",
			Containers: []corev1.Container{{Name: "guard", Image: "wao.test/guard"}}},
	}
	if err := s.c.Create(s.ctx, guard); err != nil {
		t.Fatal(err)
	}
	setReady := func(ready corev1.ConditionStatus) {
		t.Helper()
		guard.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}
		if err := s.c.Status().Update(s.ctx, guard); err != nil {
			t.Fatal(err)
		}
	}
	setAllowed(0)
	setReady(corev1.ConditionTrue)
	wantRejected(t, create(), quorumText)
	setReady(corev1.ConditionFalse)
	if err := create(); err != nil {
		t.Errorf("a control plane node with a guard Pod that is not ready was rejected: %v", err)
	}
	// The rule is only for control plane nodes.
	if err := s.c.Create(s.ctx, testNM("wao-worker", "worker-a"), client.DryRunAll); err != nil {
		t.Errorf("a worker node was rejected with no allowed etcd disruption: %v", err)
	}
}

func (s *admissionSuite) update(t *testing.T) {
	stored := &nmoapi.NodeMaintenance{}
	if err := s.c.Get(s.ctx, client.ObjectKey{Name: "wao-first"}, stored); err != nil {
		t.Fatal(err)
	}
	moved := stored.DeepCopy()
	moved.Spec.NodeName = "worker-a"
	wantRejected(t, s.c.Update(s.ctx, moved), "updating spec.NodeName isn't allowed")
	stored.Spec.Reason = "changed"
	if err := s.c.Update(s.ctx, stored); err != nil {
		t.Errorf("a change of the reason was rejected: %v", err)
	}
}

func (s *admissionSuite) noListener(t *testing.T) {
	kept := &nmoapi.NodeMaintenance{}
	if err := s.c.Get(s.ctx, client.ObjectKey{Name: "wao-first"}, kept); err != nil {
		t.Fatal(err)
	}
	s.shutdown(t)

	wantRejected(t, s.c.Create(s.ctx, testNM("wao-no-backend", "worker-a"), client.DryRunAll), "failed calling webhook")
	changed := kept.DeepCopy()
	changed.Spec.Reason = "no backend"
	wantRejected(t, s.c.Update(s.ctx, changed), "failed calling webhook")

	// The status subresource has no registration, so it needs no backend.
	kept.Status.Phase = nmoapi.MaintenanceRunning
	if err := s.c.Status().Update(s.ctx, kept); err != nil {
		t.Errorf("the status update without a listener failed: %v", err)
	}
	// The registration has no DELETE operation, so a delete needs no backend.
	if err := s.c.Delete(s.ctx, kept); err != nil {
		t.Errorf("the delete without a listener failed: %v", err)
	}
}
