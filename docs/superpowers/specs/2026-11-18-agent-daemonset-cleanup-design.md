# Design: clean up agent DaemonSets when SNR/SBR are disabled

Date: 2026-11-18
Status: proposed

## Intent and why

SNR (self-node-remediation) and SBR (storage-based-remediation) are unusual
among the WAO operands: when enabled and given a config CR, their managers
deploy a node-agent **DaemonSet**. Today, disabling the component stops the
manager but leaves that DaemonSet running. The privileged agents keep running
on every node with nothing reconciling them. The consistent, least-surprising
behavior is for a disabled component to leave no running agents behind.

This reverses the original project stance that disabling only stops the manager
and *reports* leftover agents. That stance was a deliberately cautious starting
position, not a permanent requirement; we are now choosing active cleanup of the
agent DaemonSet as the correct, consistent behavior.

## Goals

- When SNR or SBR is `Disabled`, reconcile deletes its agent DaemonSet(s) in the
  install namespace.
- Identify the DaemonSet(s) by a **pre-existing** label so the operands need no
  modification.
- Apply on steady-state disable, on transition enabled→disabled, and on WAO
  config deletion (finalize).
- Delete is idempotent: no DaemonSet present is not an error.

## Non-goals / anti-goals

- Do **not** delete or modify the component config CR
  (`SelfNodeRemediationConfig` / `StorageBasedRemediationConfig`) — the admin
  created it and may keep it. It continues to be reported as a retained resource.
- Do **not** delete native remediation objects (`SelfNodeRemediation`,
  `StorageBasedRemediation`) — still reported as retained.
- Do **not** modify, fork, or relabel the component operands (AGENTS.md hard
  constraint).
- No change to webhook `failurePolicy`; no creation of any policy/config/request.

## Constraints

- AGENTS.md: consume operands as-is; make WAO adjustments only in WAO code and
  overlays; regenerate (never hand-edit) generated RBAC/bundle files.
- WAO's own role currently has `apps/daemonsets: get;list;watch` and must gain
  `delete`. Change the kubebuilder marker in the controller, then regenerate
  `config/rbac/role.yaml` (`make manifests`) and the bundle (`make bundle-reset`).

## Verified operand facts (sibling repos)

- **SNR** (`self-node-remediation/install/self-node-remediation-deamonset.yaml`):
  single DaemonSet named `self-node-remediation-ds`, metadata label
  `k8s-app: self-node-remediation`. Owned by `SelfNodeRemediationConfig`.
- **SBR** (`storage-based-remediation` `buildDaemonSet`): one DaemonSet per
  config, named `sbr-agent-<config>`, metadata labels including
  `app: sbr-agent` (and `managed-by: sbr-operator`, `sbrconfig: <name>`). Owned
  by `StorageBasedRemediationConfig`.

## Approaches considered

1. **Match the agent DaemonSet by a pre-existing metadata label (chosen).**
   Add an agent-label selector to the component `Definition` for SNR
   (`k8s-app=self-node-remediation`) and SBR (`app=sbr-agent`). On disable, list
   DaemonSets in the install namespace matching that selector and delete them.
   Pros: exactly what was requested; needs no operand changes; labels are stable
   identities of the agent itself; works even if an ownerRef is ever missing.
   Cons: label strings are hardcoded in WAO and must track upstream if the
   operands ever rename them (low risk; covered by a test and a code comment
   pointing at the source).

2. **Reuse the existing owner-reference detection.** `retainedResources` already
   finds these DaemonSets by ownerRef to the config kind (`AgentOwners`). We
   could delete instead of report. Pros: no new field, already proven. Cons: the
   request explicitly asked for a label; ownerRef detection depends on the config
   CR still carrying the ref. Rejected in favor of the explicit label request;
   the two are equivalent in practice here since both labels and ownerRefs exist.

Decision: approach 1 — label-based matching — per the request and because the
label is the agent's own stable identity.

## How it will be built

- **Component definition** (`internal/components/component.go`): add a field, e.g.
  `AgentDaemonSetSelector map[string]string` (or a small `AgentLabels` type), set
  on `SNR()` and `SBR()` only. A code comment cites the upstream source for each
  label.
- **Controller** (`internal/controller/workloadavailabilityconfig_controller.go`):
  in `ensureDisabled`, before computing `retainedResources`, for a component with
  an agent selector, `List` DaemonSets in the install namespace with
  `client.MatchingLabels(selector)` and `Delete` each (ignore NotFound). This runs
  in the finalize path too, since `finalize` calls `ensureDisabled`.
- `retainedResources` no longer reports the agent DaemonSet (it is deleted first);
  it keeps reporting the config CR and native objects. The DaemonSet branch keyed
  on `AgentOwners` can remain as a fallback report for any DS not matched by the
  selector, or be removed — builder's discretion; default is to keep reporting
  only non-agent leftovers.
- **RBAC**: change the `apps/daemonsets` kubebuilder marker to add `delete`
  (and keep `list`); run `make manifests` and `make bundle-reset`.
- **Tests** (`..._controller_test.go`): update the existing "reports
  DisableIncomplete when SNR agents survive" spec — it currently asserts WAO must
  **not** delete the DaemonSet; invert that to assert the DaemonSet **is**
  deleted while the config CR remains reported. Add an equivalent SBR spec.
  Give the test DaemonSets the real labels above.
- **Docs**: update `docs/Overview.md` and `README.md` disable sections to state
  that SNR/SBR agent DaemonSets are removed on disable; config CRs and native
  objects are still reported as retained.

## Decided

- A component with an agent selector but a surviving config CR stays
  `DisableIncomplete`. The config CR is a real object the admin created and still
  exists, so reporting it is correct and consistent with today's behavior.

## Left to the builder

- Exact field name/type on `Definition` and whether to keep the ownerRef DS
  branch in `retainedResources` as a fallback report for non-agent leftovers.
