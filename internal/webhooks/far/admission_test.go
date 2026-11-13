package far

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	multipleTemplatesAnnotation = "remediation.medik8s.io/multiple-templates-support"
	// missingAgent is a name that is not in the test inventory.
	missingAgent = "fence_wao_missing"
)

func envFAR(name string) *farapi.FenceAgentsRemediation {
	far := testFAR()
	far.Name = name
	far.Spec.Agent = listedAgent
	return far
}

func envTemplate(name string) *farapi.FenceAgentsRemediationTemplate {
	return &farapi.FenceAgentsRemediationTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       farapi.FenceAgentsRemediationTemplateSpec{Template: farapi.FenceAgentsRemediationTemplateResource{Spec: envFAR(name).Spec}},
	}
}

func syntheticSecret(namespace, name string, data map[string]string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, StringData: data}
}

func wantDenied(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("error = %v, want a denial that contains %q", err, message)
	}
}

// admissionSuite holds the shared API server and the leader replica of TestAdmissionThroughAPIServer.
type admissionSuite struct {
	env    *admissionEnv
	ctx    context.Context
	c      client.Client
	leader *replica
}

// This test sends requests through the API server to the real registered handlers.
// The steps share one API server and run in order.
func TestAdmissionThroughAPIServer(t *testing.T) {
	env := startAdmissionEnv(t)
	s := &admissionSuite{env: env, ctx: context.Background(), c: env.client}
	s.leader = env.startReplica(t, env.options.LocalServingPort)
	eventually(t, "the first replica holds the leader lease", s.leader.elected)

	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"registrations are the four Fail entries for CREATE and UPDATE", s.registrations},
		{"CREATE of a valid FenceAgentsRemediation is admitted", s.createValid},
		{"CREATE with an agent that is not in the inventory is rejected", s.rejectUnlistedAgent},
		{"CREATE with an invalid action is rejected", s.rejectInvalidAction},
		{"CREATE without parameters is rejected", s.rejectNoParameters},
		{"an unsupported strategy is rejected by admission", s.rejectUnsupportedStrategy},
		{"a malformed object is rejected by the CRD schema, not by admission", s.schemaRejectsMalformed},
		{"template CREATE gets the annotation and keeps an existing value", s.templateAnnotation},
		{"Secret parameters come from the namespace of the resource", s.secretNamespace},
		{"the dedicated client reads the current Secret, not a cached copy", s.currentSecret},
		{"legacy Secret defaulting on CREATE and the template UPDATE restriction", s.legacySecret},
		{"concurrent requests give consistent results", s.concurrentRequests},
		{"a second replica serves the handlers without the leader lease", s.secondReplica},
		{"without a serving listener, CREATE and UPDATE fail and DELETE is not intercepted", s.noListener},
	} {
		t.Run(step.name, step.run)
	}
}

func (s *admissionSuite) registrations(t *testing.T) {
	ctx, c := s.ctx, s.c
	type entry struct {
		rules   []admissionregistrationv1.RuleWithOperations
		failure *admissionregistrationv1.FailurePolicyType
	}
	entries := map[string]entry{}
	mutating := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	validating := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := c.List(ctx, mutating); err != nil {
		t.Fatal(err)
	}
	if err := c.List(ctx, validating); err != nil {
		t.Fatal(err)
	}
	// config/webhook/manifests.yaml also has the entries of other components. This suite checks the FAR entries.
	add := func(name string, rules []admissionregistrationv1.RuleWithOperations, failure *admissionregistrationv1.FailurePolicyType) {
		if len(rules) > 0 && slices.Contains(rules[0].APIGroups, farapi.GroupVersion.Group) {
			entries[name] = entry{rules, failure}
		}
	}
	for _, cfg := range mutating.Items {
		for _, w := range cfg.Webhooks {
			add(w.Name, w.Rules, w.FailurePolicy)
		}
	}
	for _, cfg := range validating.Items {
		for _, w := range cfg.Webhooks {
			add(w.Name, w.Rules, w.FailurePolicy)
		}
	}
	if len(entries) != 4 {
		t.Fatalf("%d entries, want 4: %v", len(entries), entries)
	}
	for name, e := range entries {
		if e.failure == nil || *e.failure != admissionregistrationv1.Fail {
			t.Errorf("%s: failurePolicy = %v, want Fail", name, e.failure)
		}
		operations := fmt.Sprint(e.rules[0].Operations)
		if len(e.rules) != 1 || operations != "[CREATE UPDATE]" {
			t.Errorf("%s: operations = %s, want CREATE and UPDATE only", name, operations)
		}
	}
}

func (s *admissionSuite) createValid(t *testing.T) {
	ctx, c := s.ctx, s.c
	if err := c.Create(ctx, envFAR("wao-valid"), client.DryRunAll); err != nil {
		t.Fatal(err)
	}
}

func (s *admissionSuite) rejectUnlistedAgent(t *testing.T) {
	ctx, c := s.ctx, s.c
	for _, agent := range []string{missingAgent, "./fence_ipmilan", "fence_wao/../../bin/sh"} {
		far := envFAR("wao-reject-agent")
		far.Spec.Agent = agent
		wantDenied(t, c.Create(ctx, far, client.DryRunAll), "unsupported fence agent: "+agent)
		tmpl := envTemplate("wao-reject-agent")
		tmpl.Spec.Template.Spec.Agent = agent
		wantDenied(t, c.Create(ctx, tmpl, client.DryRunAll), "unsupported fence agent: "+agent)
	}
}

func (s *admissionSuite) rejectInvalidAction(t *testing.T) {
	ctx, c := s.ctx, s.c
	far := envFAR("wao-reject-action")
	far.Spec.SharedParameters = map[farapi.ParameterName]string{"--action": "shutdown"}
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), farapi.ErrorUnsupportedAction)
}

func (s *admissionSuite) rejectNoParameters(t *testing.T) {
	ctx, c := s.ctx, s.c
	tmpl := envTemplate("wao-reject-empty")
	tmpl.Spec.Template.Spec.NodeParameters = nil
	wantDenied(t, c.Create(ctx, tmpl, client.DryRunAll), "mandatory parameters are missing")
}

func (s *admissionSuite) rejectUnsupportedStrategy(t *testing.T) {
	ctx, c := s.ctx, s.c
	far := envFAR("wao-strategy")
	far.Spec.RemediationStrategy = farapi.OutOfServiceTaintRemediationStrategy
	if err := c.Create(ctx, far, client.DryRunAll); err != nil {
		t.Fatalf("OutOfServiceTaint was rejected on a cluster that supports it: %v", err)
	}
	// The envtest API server is newer than 1.26. Set the state of an older cluster for this check.
	farapi.SetOutOfServiceTaintSupported(false)
	defer farapi.SetOutOfServiceTaintSupported(true)
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), "OutOfServiceTaint remediation strategy is not supported")
}

func (s *admissionSuite) schemaRejectsMalformed(t *testing.T) {
	ctx, c := s.ctx, s.c
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(farapi.GroupVersion.String())
	u.SetKind("FenceAgentsRemediation")
	u.SetNamespace(testNamespace)
	u.SetName("wao-malformed")
	if err := unstructured.SetNestedField(u.Object, listedAgent, "spec", "agent"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(u.Object, "NotAStrategy", "spec", "remediationStrategy"); err != nil {
		t.Fatal(err)
	}
	err := c.Create(ctx, u, client.DryRunAll)
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "spec.remediationStrategy") {
		t.Fatalf("error = %v, want a schema error for spec.remediationStrategy", err)
	}
}

func (s *admissionSuite) templateAnnotation(t *testing.T) {
	ctx, c := s.ctx, s.c
	added := envTemplate("wao-annotation-added")
	if err := c.Create(ctx, added, client.DryRunAll); err != nil {
		t.Fatal(err)
	}
	if added.Annotations[multipleTemplatesAnnotation] != "true" {
		t.Errorf("annotations = %v, want %s=true", added.Annotations, multipleTemplatesAnnotation)
	}
	kept := envTemplate("wao-annotation-kept")
	kept.Annotations = map[string]string{multipleTemplatesAnnotation: "false"}
	if err := c.Create(ctx, kept, client.DryRunAll); err != nil {
		t.Fatal(err)
	}
	if kept.Annotations[multipleTemplatesAnnotation] != "false" {
		t.Errorf("annotations = %v, want the existing value false", kept.Annotations)
	}
}

func (s *admissionSuite) secretNamespace(t *testing.T) {
	ctx, c := s.ctx, s.c
	// The Secret in the other namespace has an invalid action. A read from that namespace is a denial.
	for _, s := range []*corev1.Secret{
		syntheticSecret(testNamespace, "wao-shared", map[string]string{"--username": "synthetic-user"}),
		syntheticSecret("far-other", "wao-shared", map[string]string{"--action": "shutdown"}),
		syntheticSecret(testNamespace, "wao-bad-action", map[string]string{"--action": "shutdown"}),
	} {
		if err := c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	far := envFAR("wao-secret")
	far.Spec.SharedSecretName = ptr.To("wao-shared")
	if err := c.Create(ctx, far, client.DryRunAll); err != nil {
		t.Fatal(err)
	}
	far.Spec.SharedSecretName = ptr.To("wao-bad-action")
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), farapi.ErrorUnsupportedAction)
	far.Spec.SharedSecretName = ptr.To("wao-absent")
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), "secret 'wao-absent' not found in namespace '"+testNamespace+"'")
}

func (s *admissionSuite) currentSecret(t *testing.T) {
	ctx, c := s.ctx, s.c
	far := envFAR("wao-current-secret")
	far.Spec.SharedSecretName = ptr.To("wao-changing")
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), "secret 'wao-changing' not found")
	secret := syntheticSecret(testNamespace, "wao-changing", map[string]string{"--username": "synthetic-user"})
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, far, client.DryRunAll); err != nil {
		t.Fatalf("the request after the Secret creation was rejected: %v", err)
	}
	secret.StringData = map[string]string{"--action": "shutdown"}
	if err := c.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, c.Create(ctx, far, client.DryRunAll), farapi.ErrorUnsupportedAction)
}

func (s *admissionSuite) legacySecret(t *testing.T) {
	ctx, c := s.ctx, s.c
	legacy := syntheticSecret(testNamespace, farapi.OldDefaultSecretName, map[string]string{"--username": "synthetic-user"})
	if err := c.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	tmpl := envTemplate("wao-legacy")
	far := envFAR("wao-legacy")
	if err := c.Create(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, far); err != nil {
		t.Fatal(err)
	}
	for kind, ref := range map[string]*string{"template": tmpl.Spec.Template.Spec.SharedSecretName, "FAR": far.Spec.SharedSecretName} {
		if ref == nil || *ref != farapi.OldDefaultSecretName {
			t.Fatalf("%s CREATE: reference = %v, want the legacy name", kind, ref)
		}
	}

	// Template UPDATE: the reference cannot be removed while the legacy Secret exists.
	tmpl.Spec.Template.Spec.SharedSecretName = nil
	wantDenied(t, c.Update(ctx, tmpl), "shared secret with the deprecated default name")
	// FenceAgentsRemediation UPDATE has no such restriction, and UPDATE does not add the name again.
	far.Spec.SharedSecretName = nil
	if err := c.Update(ctx, far); err != nil {
		t.Fatalf("FAR UPDATE was rejected: %v", err)
	}
	if far.Spec.SharedSecretName != nil {
		t.Errorf("FAR UPDATE set the reference to %q", *far.Spec.SharedSecretName)
	}

	// Without the legacy Secret, the template UPDATE is admitted.
	if err := c.Delete(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(tmpl), tmpl); err != nil {
		t.Fatal(err)
	}
	tmpl.Spec.Template.Spec.SharedSecretName = nil
	if err := c.Update(ctx, tmpl); err != nil {
		t.Fatalf("template UPDATE without the legacy Secret was rejected: %v", err)
	}
	// An invalid UPDATE of an existing object is rejected.
	tmpl.Spec.Template.Spec.Agent = missingAgent
	wantDenied(t, c.Update(ctx, tmpl), "unsupported fence agent")
	far.Spec.Agent = missingAgent
	wantDenied(t, c.Update(ctx, far), "unsupported fence agent")
}

func (s *admissionSuite) concurrentRequests(t *testing.T) {
	ctx, c := s.ctx, s.c
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 10 {
				far := envFAR(fmt.Sprintf("wao-concurrent-%d-%d", worker, i))
				if i%2 == 1 {
					far.Spec.Agent = missingAgent
				}
				err := c.Create(ctx, far, client.DryRunAll)
				if (i%2 == 0) != (err == nil) {
					t.Errorf("agent %s: error = %v", far.Spec.Agent, err)
				}
			}
		})
	}
	wg.Wait()
}

func (s *admissionSuite) secondReplica(t *testing.T) {
	second := s.env.startReplica(t, freePort(t))
	if second.elected() {
		t.Fatal("the second replica holds the leader lease")
	}
	if !s.leader.elected() {
		t.Fatal("the first replica lost the leader lease")
	}
	resp := reviewHTTPS(t, second.addr, farPaths[1], "FenceAgentsRemediation", envFAR("wao-second"))
	if !resp.Allowed {
		t.Errorf("the second replica denied a valid request: %v", resp.Result)
	}
	rejected := envFAR("wao-second")
	rejected.Spec.Agent = missingAgent
	resp = reviewHTTPS(t, second.addr, farPaths[1], "FenceAgentsRemediation", rejected)
	if resp.Allowed || !strings.Contains(resp.Result.Message, "unsupported fence agent") {
		t.Errorf("the second replica did not use the inventory: %+v", resp.Result)
	}
	resp = reviewHTTPS(t, second.addr, farPaths[2], "FenceAgentsRemediationTemplate", envTemplate("wao-second"))
	if !resp.Allowed || !strings.Contains(string(resp.Patch), "multiple-templates-support") {
		t.Errorf("the second replica did not default the template: allowed %v, patch %s", resp.Allowed, resp.Patch)
	}
	if second.elected() {
		t.Error("the second replica took the leader lease during the test")
	}
	second.shutdown(t)
}

func (s *admissionSuite) noListener(t *testing.T) {
	ctx, c := s.ctx, s.c
	s.leader.shutdown(t)
	wantDenied(t, c.Create(ctx, envFAR("wao-no-backend"), client.DryRunAll), "failed calling webhook")
	wantDenied(t, c.Create(ctx, envTemplate("wao-no-backend"), client.DryRunAll), "failed calling webhook")

	tmpl := &farapi.FenceAgentsRemediationTemplate{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "wao-legacy"}, tmpl); err != nil {
		t.Fatal(err)
	}
	tmpl.Labels = map[string]string{"wao-test": "update"}
	wantDenied(t, c.Update(ctx, tmpl), "failed calling webhook")

	// No registration matches DELETE, so the deletion needs no backend.
	if err := c.Delete(ctx, tmpl); err != nil {
		t.Fatalf("template DELETE failed without a backend: %v", err)
	}
	far := &farapi.FenceAgentsRemediation{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "wao-legacy"}}
	if err := c.Delete(ctx, far); err != nil {
		t.Fatalf("FAR DELETE failed without a backend: %v", err)
	}
}
