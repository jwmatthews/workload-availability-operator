# Agent DaemonSet Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When SNR or SBR is Disabled, WAO reconcile deletes the component's agent DaemonSet(s) in the install namespace, matched by a pre-existing operand label.

**Architecture:** Add an `AgentDaemonSetSelector` label map to the component `Definition`, set only on SNR and SBR. In `ensureDisabled` (which also runs from `finalize`), list DaemonSets in the install namespace matching that selector and delete them before computing retained resources. The surviving admin-created config CR and native objects are still reported as retained (`DisableIncomplete`).

**Tech Stack:** Go, controller-runtime, Ginkgo/Gomega (envtest), controller-gen, operator-sdk bundle.

**Spec:** `docs/superpowers/specs/2026-11-18-agent-daemonset-cleanup-design.md`

## Global Constraints

- Do not modify, fork, or relabel the component operands; consume as-is (AGENTS.md).
- Never hand-edit generated files: `config/rbac/role.yaml` (regen via `make manifests`), `bundle/` (regen via `make bundle-reset`), `internal/operands/manifests/*` (regen via `make operand-manifests`).
- Never change a component webhook `failurePolicy`; create no policy/config/request.
- Work on a new branch `improve-cleanup` based on `upstream/wao`.
- Verified operand labels (from sibling repos, do not change them): SNR agent DaemonSet metadata label `k8s-app: self-node-remediation` (name `self-node-remediation-ds`); SBR agent DaemonSet metadata label `app: sbr-agent` (name `sbr-agent-<config>`, one per config).

## Review Focus

- A component with no agent selector (NHC, FAR, MDR, NMO) must be unaffected by the delete path — covered by Task 2's "unrelated DaemonSet is not deleted" assertion.
- A DaemonSet in the install namespace that does **not** match the agent label must survive a disable — covered by Task 2 (create a foreign DS with a different label, assert it remains).
- Disable when the agent DaemonSet is already absent must not error (idempotent) — covered by Task 2's SBR spec which disables with no DS present at least once via Eventually re-reconcile.
- Multiple SBR agent DaemonSets (one per config) must all be deleted — covered by Task 2's SBR spec creating two `sbr-agent-*` DaemonSets.
- RBAC must actually permit delete on a live cluster — covered by Task 3 asserting `config/rbac/role.yaml` lists `delete` under `apps/daemonsets` after regeneration.

---

### Task 1: Declare the agent DaemonSet selector on SNR and SBR

**Files:**
- Modify: `internal/components/component.go` (add field to `Definition`)
- Modify: `internal/components/snr.go`, `internal/components/sbr.go`
- Test: `internal/components/components_test.go`

**Interfaces:**
- Produces: `Definition.AgentDaemonSetSelector map[string]string` — the label set uniquely identifying a component's runtime agent DaemonSet(s) in the install namespace; empty for components with no agents. SNR → `{"k8s-app": "self-node-remediation"}`, SBR → `{"app": "sbr-agent"}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/components/components_test.go`:

```go
func TestSNRAndSBRDeclareAgentDaemonSetSelector(t *testing.T) {
	snr, _ := components.ByKey(v1alpha1.ComponentKeySelfNodeRemediation)
	sbr, _ := components.ByKey(v1alpha1.ComponentKeyStorageBasedRemediation)
	nhc, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	if snr.AgentDaemonSetSelector["k8s-app"] != "self-node-remediation" {
		t.Errorf("SNR agent DaemonSet selector = %+v", snr.AgentDaemonSetSelector)
	}
	if sbr.AgentDaemonSetSelector["app"] != "sbr-agent" {
		t.Errorf("SBR agent DaemonSet selector = %+v", sbr.AgentDaemonSetSelector)
	}
	if len(nhc.AgentDaemonSetSelector) != 0 {
		t.Errorf("NHC has no agent DaemonSet, got %+v", nhc.AgentDaemonSetSelector)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/components/ -run TestSNRAndSBRDeclareAgentDaemonSetSelector`
Expected: FAIL (field `AgentDaemonSetSelector` undefined).

- [ ] **Step 3: Add the field and set it**

In `internal/components/component.go`, add to `Definition` a field with a comment citing the upstream source:

```go
// AgentDaemonSetSelector matches, by pre-existing operand labels, the runtime agent
// DaemonSet(s) a component creates in the install namespace. When the component is
// Disabled these DaemonSets are deleted. Empty for components with no node agent.
AgentDaemonSetSelector map[string]string
```

In `snr.go` set `AgentDaemonSetSelector: map[string]string{"k8s-app": "self-node-remediation"}` (label from `self-node-remediation/install/self-node-remediation-deamonset.yaml`). In `sbr.go` set `AgentDaemonSetSelector: map[string]string{"app": "sbr-agent"}` (label from `storage-based-remediation` `buildDaemonSet`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/components/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/components/
git commit -m "components: declare agent DaemonSet selector for SNR and SBR"
```

---

### Task 2: Delete agent DaemonSets on disable

**Files:**
- Modify: `internal/controller/workloadavailabilityconfig_controller.go` (`ensureDisabled`, kubebuilder RBAC marker)
- Test: `internal/controller/workloadavailabilityconfig_controller_test.go`

**Interfaces:**
- Consumes: `Definition.AgentDaemonSetSelector` from Task 1.

- [ ] **Step 1: Rewrite the SNR spec and add an SBR spec (failing)**

In `workloadavailabilityconfig_controller_test.go`, replace the existing `It("reports DisableIncomplete when SNR agents survive a manager-level disable", ...)` spec so that, after disabling SNR, it asserts the agent DaemonSet is **deleted** while the config CR is **still reported**:

```go
It("deletes the SNR agent DaemonSet on disable but keeps reporting the config CR", func() {
	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(schema.GroupVersionKind{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediationConfig"})
	cfg.SetName("self-node-remediation-config")
	cfg.SetNamespace(ns)
	Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "self-node-remediation-ds", Namespace: ns,
		Labels: map[string]string{"k8s-app": "self-node-remediation"}},
		Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "self-node-remediation"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "self-node-remediation"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "example.test/agent"}}}}}}
	Expect(k8sClient.Create(ctx, ds)).To(Succeed())
	// A foreign DaemonSet with a different label must survive.
	foreign := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-ds", Namespace: ns,
		Labels: map[string]string{"app": "something-else"}},
		Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "something-else"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "something-else"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "example.test/x"}}}}}}
	Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

	setEnabled(ctx, v1alpha1.ComponentKeySelfNodeRemediation, true)
	Eventually(func(g Gomega) {
		g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeySelfNodeRemediation).Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
	}).Should(Succeed())

	setEnabled(ctx, v1alpha1.ComponentKeySelfNodeRemediation, false)
	Eventually(func(g Gomega) {
		g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), &appsv1.DaemonSet{}))).To(BeTrue())
		c := component(getWAC(ctx), v1alpha1.ComponentKeySelfNodeRemediation)
		g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisableIncomplete))
		g.Expect(strings.Join(c.RetainedResources, "\n")).To(ContainSubstring("SelfNodeRemediationConfig " + ns + "/self-node-remediation-config"))
		g.Expect(strings.Join(c.RetainedResources, "\n")).NotTo(ContainSubstring("self-node-remediation-ds"))
	}).Should(Succeed())
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foreign), foreign)).To(Succeed(), "foreign DaemonSet must survive")
	Expect(k8sClient.Delete(ctx, foreign)).To(Succeed())
	Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
})
```

Add a sibling spec for SBR that creates two agent DaemonSets (`sbr-agent-a`, `sbr-agent-b`, both labeled `app: sbr-agent`) plus a `StorageBasedRemediationConfig`, enables then disables SBR, and asserts both DaemonSets are deleted and the config CR is still reported. (SBR has no webhooks, so the enable→Progressing step mirrors SNR without admission concerns.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test` (or `go test ./internal/controller/...` if envtest assets are already fetched)
Expected: FAIL — the DaemonSet is still present / still reported.

- [ ] **Step 3: Add the delete path and RBAC marker**

In `ensureDisabled`, after the existing render-and-delete loop and before `retainedResources`, add:

```go
if len(def.AgentDaemonSetSelector) > 0 {
	dsl := &appsv1.DaemonSetList{}
	if err := r.List(ctx, dsl, client.InNamespace(r.Namespace), client.MatchingLabels(def.AgentDaemonSetSelector)); err != nil {
		return st, err
	}
	for i := range dsl.Items {
		ds := &dsl.Items[i]
		if err := r.Delete(ctx, ds); err != nil && !apierrors.IsNotFound(err) {
			return st, fmt.Errorf("delete DaemonSet %s/%s: %w", ds.Namespace, ds.Name, err)
		}
	}
}
```

Change the kubebuilder RBAC marker for daemonsets (currently `// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch`) to add `delete`:

```go
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;delete
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test`
Expected: PASS (both SNR and SBR specs).

- [ ] **Step 5: Commit**

```bash
git add internal/controller/workloadavailabilityconfig_controller.go internal/controller/workloadavailabilityconfig_controller_test.go
git commit -m "controller: delete SNR/SBR agent DaemonSets on disable"
```

---

### Task 3: Regenerate RBAC and bundle, update docs

**Files:**
- Regenerate: `config/rbac/role.yaml` (`make manifests`), `bundle/` (`make bundle-reset`)
- Modify: `docs/Overview.md`, `README.md`

**Interfaces:**
- Consumes: the RBAC marker change from Task 2.

- [ ] **Step 1: Regenerate RBAC and bundle**

Run: `make manifests` then `make bundle-reset`

- [ ] **Step 2: Verify the regenerated RBAC grants delete**

Run: `grep -A8 'resources:\n  - daemonsets' config/rbac/role.yaml` (or inspect the `daemonsets` rule).
Expected: the `apps/daemonsets` rule lists `delete` alongside `get`, `list`, `watch`. Confirm the bundle CSV changed consistently and `git status` shows no unexpected drift.

- [ ] **Step 3: Update the docs**

In `README.md` (the "What Enabled and Disabled mean" section) and `docs/Overview.md` (the disable/retain paragraph around lines 72–74), change the text that says SNR/SBR agent DaemonSets stay on disable: state that WAO now deletes the SNR and SBR agent DaemonSets on disable, while the config CR, native objects and other operand-created objects are still reported as `DisableIncomplete` in `retainedResources`.

- [ ] **Step 4: Run the full verification suite**

Run: `make check` then `make test-offline` then `make test`
Expected: all pass; `verify-no-changes` clean (no stale generated files).

- [ ] **Step 5: Commit**

```bash
git add config/rbac/role.yaml bundle/ docs/Overview.md README.md
git commit -m "rbac,docs: permit DaemonSet delete and document SNR/SBR agent cleanup"
```

---

## Notes for the executor

- Branch setup (non-standard base): `git fetch upstream && git switch -c improve-cleanup upstream/wao` before Task 1. The design doc should be carried onto this branch (cherry-pick or re-add) so it travels with the work.
- The existing `TestSNRAndSBRDeclareAgentOwners` and the owner-reference DaemonSet branch in `retainedResources` stay as-is; they continue to report any non-agent leftovers. The agent DaemonSets are simply deleted before that reporting runs, so they drop out of the retained list naturally.
