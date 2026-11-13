# Plan: NMO admission webhook in the WAO manager

Date: 2026-11-13. Branch: `feat/webhooks/nmo`, from `feat/webhooks/nhc` at `b2901af`.

This file is the plan. The implementation follows section 5.
The goal is that the WAO manager serves the Node Maintenance Operator (NMO) admission webhook, with the same structure as the FAR and NHC work.
[The NHC plan](2026-11-13-nhc-webhooks-in-wao.md) is the template. Its section 2 describes the shared structure, and this plan does not repeat it.

Terms in this plan:

- **Registration**: the `ValidatingWebhookConfiguration` entry that tells the API server to call a webhook.
- **Central admission**: admission that the WAO manager serves, with a registration that OLM installs from the WAO CSV.
- **Quorum rule**: the NMO rule that rejects a `NodeMaintenance` for a control plane node when the node drain can break the etcd quorum.

Decision identifiers in this plan have the prefix `N` (N1, N2, ...). The NHC plan uses D1 to D4.

## 1. NMO webhook upstream

### Source pin

| Item | Value |
| --- | --- |
| Repository | <https://github.com/medik8s/node-maintenance-operator> |
| Local checkout | `../node-maintenance-operator` (read-only for this task) |
| Branch | `wao` (same revision on `upstream/wao`) |
| Revision | `5baa709167a2325aa61be37417fc692a69cf27e6` (`v0.21.0-22-g5baa7091`) |
| Go module | `github.com/medik8s/node-maintenance-operator/v5` |
| Expected module version | `v5.0.0-20261013171824-5baa709167a2`. Check that the Go module proxy resolves it. |
| NMO `go.mod` | Go 1.26.0, controller-runtime v0.24.1, `k8s.io/api` v0.36.2, `medik8s/common` v1.17.0 |

WAO already has these versions or newer patch versions. No separate dependency update is expected.

### Webhook inventory

NMO has one webhook. It has no mutating webhook, no defaulter and no conversion webhook. The API has one version, `v1beta1`.
`config/operands/nmo/config/webhook/manifests.yaml` agrees with this table.

| Item | Value |
| --- | --- |
| Type | Validating |
| Entry name | `vnodemaintenance.kb.io` |
| Group, version, kind | `nodemaintenance.medik8s.io`, `v1beta1`, `NodeMaintenance` (cluster-scoped) |
| Resource | `nodemaintenances` (not `nodemaintenances/status`) |
| Path | `/validate-nodemaintenance-medik8s-io-v1beta1-nodemaintenance` |
| Operations | `CREATE`, `UPDATE` |
| `failurePolicy` | `Fail` |
| `sideEffects` | `None` |
| AdmissionReview versions | `v1` |

### Upstream files

| File | Content |
| --- | --- |
| `internal/webhook/v1beta1/nodemaintenance_webhook.go` (182 lines) | `SetupNodeMaintenanceWebhookWithManager(isOpenShift, mgr)`, the marker, the validator and all rules |
| `internal/webhook/v1beta1/nodemaintenance_webhook_test.go` (259 lines) | Tests through an envtest API server |
| `internal/webhook/v1beta1/webhook_suite_test.go` (149 lines) | envtest suite |
| `api/v1beta1` | Types (public, importable) |
| `pkg/utils/validation.go` | `NewOpenshiftValidator` (public, importable). It uses API discovery to find the group version `config.openshift.io/v1`. |
| `medik8s/common` `pkg/etcd`, `pkg/nodes` | `IsEtcdDisruptionAllowed`, `IsControlPlane`. WAO already requires `medik8s/common` v1.17.0. |
| `cmd/main.go` | Calls the setup function with no condition. There is no flag that disables the webhook. |

### Validation rules

1. `CREATE`: a Node with the name `spec.nodeName` must exist.
2. `CREATE`: no other `NodeMaintenance` has the same `spec.nodeName`.
3. `CREATE`, only on OpenShift, only for a control plane node (quorum rule):
   - The handler lists the PodDisruptionBudgets in the namespace `openshift-etcd`. Zero or more than one gives a rejection.
   - If `status.disruptionsAllowed` is 1 or more, the request passes.
   - If not, the handler lists the Pods that the PodDisruptionBudget selects. The request passes only if the guard Pod of the node is absent or not ready.
4. `UPDATE`: `spec.nodeName` cannot change.
5. `DELETE` is not in the registration. The `ValidateDelete` function exists and returns no error.

### Dependencies of the webhook

| Dependency | Use | Access to the cluster |
| --- | --- | --- |
| Client | Rule 1 and rule 3 | `get` on `nodes` |
| Client | Rule 2 | `list` on `nodemaintenances`. The WAO ServiceAccount has this permission now. |
| Client | Rule 3 | `list` on `poddisruptionbudgets` and `list` on `pods`, both in `openshift-etcd` only |
| `isOpenShift` | Rule 3 | API discovery, one time at manager start |

The WAO ClusterRole has no rule for `nodes`, `pods` or `poddisruptionbudgets` now.
The webhook reads no Secret. It writes the object name, the node name and the rejection text to the log.

## 2. Answers to the handoff questions

| # | Question | Answer for NMO |
| --- | --- | --- |
| 1 | Entries, operations, `failurePolicy` | One validating entry, `CREATE` and `UPDATE`, `Fail`. See section 1. |
| 2 | Copied files, new dependencies | The three files of `internal/webhook/v1beta1/`. WAO imports `api/v1beta1` and `pkg/utils`. The NMO module is the only new requirement. Check the effect of `go mod tidy` before the commit. |
| 3 | Objects that a handler reads, permissions | Node, `NodeMaintenance` list, and the PodDisruptionBudget and Pods of `openshift-etcd`. Decision N1 gives the scope of the new permissions. |
| 4 | Read of cluster state at start | Yes: API discovery for the OpenShift check. A failure stops each WAO replica, also when NMO is `Disabled`. NHC admission already uses API discovery at start, so the dependency is not new ([issue 13](https://github.com/jwmatthews/workload-availability-operator/issues/13)). Decision N2. |
| 5 | Rule that depends on state of the operand controller | Rule 2 only. A `NodeMaintenance` that the controller cannot complete or remove stays while NMO is `Disabled`, and it blocks a second object for the same node. The user can delete the first object if it has no finalizer, or enable NMO. No rule blocks a delete or an update that a user needs. Decision N3: no `AdmissionBlock`. |
| 6 | `DELETE` in an entry | No. A delete needs no WAO replica. |
| 7 | Secret data, values in the log | No Secret read. The log has object names and node names only. |
| 8 | Unused operand listener | Yes. The NMO image always starts its webhook server and needs certificate files. NMO has no flag to stop it. Decision N4. |
| 9 | Mutating entry | None. |

## 3. Steps for NMO

### 3.1 What WAO can use again without a change

- `internal/webhooks/listener`: the dedicated client and the readiness check `admission`.
- The webhook server, the certificate path, 2 replicas, `config/webhook/`, the Makefile bundle steps.
- The `CentralAdmission` field and the controller status logic.
- `ENVTEST_PACKAGES` and the race detector run use `./internal/webhooks/...`. No Makefile change is expected.

NMO needs no init container, no `AdmissionBlock`, no new status logic and no purge script change.

### 3.2 Source and copies

1. Add the NMO module to `go.mod`. Run `go mod tidy` and `go mod vendor`.
2. Import `api/v1beta1` and `pkg/utils` from the module. Do not copy them.
3. Copy these files and keep the license headers:

| Upstream file | WAO destination | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1beta1/nodemaintenance_webhook.go` | `internal/webhooks/nmo/v1beta1/` | `SetupNodeMaintenanceWebhookWithManager` receives `admissionClient client.Client`. The validator uses that client instead of `mgr.GetClient()`. Marker and rules unchanged. |
| `internal/webhook/v1beta1/nodemaintenance_webhook_test.go` | same | None expected |
| `internal/webhook/v1beta1/webhook_suite_test.go` | same | CRD path `config/operands/nmo/config/crd/bases`, webhook path `config/webhook`, missing CRD path is an error, setup call |

4. Write `internal/webhooks/nmo/SOURCE.md` with the pin, the table above and the `diff` command.
5. Add `internal/webhooks/nmo/v1beta1` to the lint exclusions in `.golangci.yml`.

Difference from upstream: NMO gives the validator the manager client, which reads from a cache. WAO gives it the dedicated client, which reads from the API server.
Thus rule 2 sees a `NodeMaintenance` that a cache does not have yet. Each `CREATE` makes 2 to 5 direct read requests.

### 3.3 Setup package `internal/webhooks/nmo`

- `setup.go` with `Options{AdmissionClient, IsOpenShift}`, `SetupWithManager` and `Initialize(mgr)`.
- `Initialize` does these steps. A failure of a step stops the manager, also when NMO is `Disabled`:
  1. Make the dedicated client with `listener.NewAdmissionClient`.
  2. Call `utils.NewOpenshiftValidator(mgr.GetConfig())` (decision N2).
  3. Register the handler.
- RBAC markers in `setup.go`, with the scope of decision N1: `get` on `nodes`, `list` on `poddisruptionbudgets`, `list` on `pods`.
- `cmd/main.go`: add the NMO API to the scheme. Call `nmowebhooks.Initialize(mgr)` after the NHC call and before `listener.AddReadyCheck`.

### 3.4 Registration

1. Run `make manifests`. `config/webhook/manifests.yaml` gets a sixth entry, `vnodemaintenance.kb.io`. `config/rbac/role.yaml` gets the new rules.
2. Run `make bundle-reset`. The CSV gets a sixth `webhookdefinitions` entry with the WAO Deployment name.
3. Check that the entry has `failurePolicy: Fail` and the operations `CREATE` and `UPDATE` only.

### 3.5 Operand manifests

1. Add `config/operands/nmo/overlays/runtime/webhook_configurations_delete_patch.yaml` with `$patch: delete` for `node-maintenance-operator-validating-webhook-configuration`.
2. Remove `webhook_validating_patch.yaml` from the overlay and from its `kustomization.yaml`. Update the header comment.
3. Keep `webhook_cert_patch.yaml` and `webhook_service_patch.yaml` (decision N4).
4. Run `make operand-manifests`. Do not edit `internal/operands/manifests/nmo/objects.yaml` directly.
5. Do not edit `config/operands/nmo/config/`.

### 3.6 Component definition

- `internal/components/nmo.go`: remove `Webhooks`, add `CentralAdmission: true`. No `AdmissionBlock` (decision N3).
- Result in the status of `nodeMaintenance`: `admissionReady` shows the local listener in both states.

### 3.7 Tests

| Test | Change |
| --- | --- |
| `internal/webhooks/nmo/v1beta1/` | Copied upstream tests. Add `wao_behavior_test.go` with a fake client for the paths that the upstream tests do not cover: a failed Node read, a failed list, two PodDisruptionBudgets, a cluster that is not OpenShift. |
| `internal/webhooks/nmo/setup_test.go` | Nil manager, nil client, registration error, discovery error at start |
| `internal/webhooks/nmo/admission_test.go` | Through an envtest API server with the real registration and a user that has only the generated ClusterRole: valid `CREATE`, each rejection of rules 1 to 4, a delete with no listener passes, a status update gets no validation, second replica, `Fail` with no listener |
| `cmd/main_test.go` | One NMO admission request to the built binary |
| `test/offline/nmo_admission_test.go` | Marker equals CSV definition. The definition points at the WAO Deployment. No registration in the NMO runtime objects. The Service and the certificate Secret name stay. The WAO role has only the planned verbs on `nodes`, `pods` and `poddisruptionbudgets`. |
| `test/offline/far_admission_test.go`, `nhc_admission_test.go` | Update each count of entries and each list of operands that have no central admission |
| `internal/components/components_test.go` | NMO has 0 webhooks and `CentralAdmission` true. The test name includes NMO. |
| `internal/operands/render_test.go`, `internal/controller/*_test.go` | Remove each expectation of the NMO registration. Update the comment of the test `reports a NodeMaintenance left behind by a disabled NMO as retained`. |

### 3.8 Documents

- `docs/nmo-webhooks.md` with the structure of `docs/nhc-webhooks.md`: entry table, ownership, startup, permissions, behavior that WAO preserves, differences from standalone NMO, source pin update, dry-run procedure, acceptance results.
- Update `docs/operand-config-overlays.md`, `docs/Overview.md` and `README.md` where they list the operands with central admission.
- `HANDOFF.md` is not in git. This branch does not change it.

### 3.9 Cluster acceptance

The user named the cluster in this session. The kubeconfig path is not written in a committed file.
Each command gets the kubeconfig explicitly. All admission requests use server-side dry-run. The test creates no `NodeMaintenance`.

1. Remove a previous WAO installation (`make dev-olm-undeploy`, then `hack/wao-purge.sh` if objects stay).
2. Install with `make dev-olm-deploy`. Check that the six components are `Disabled` and that no operand Pod runs.
3. Check the registration: entry name, `Fail`, `CREATE` and `UPDATE`, Service of the WAO Deployment.
4. Check the permissions of the WAO ServiceAccount with `kubectl auth can-i`.
5. Send these dry-run `CREATE` requests in each state (`Disabled`, `Enabled`, `Disabled` again, after deletion of the `WorkloadAvailabilityConfig`):
   - a node name that does not exist: expect rule 1 to reject,
   - a worker node: expect a pass,
   - a control plane node: read `status.disruptionsAllowed` of the etcd PodDisruptionBudget first, then compare the answer with rule 3.
6. Enable the components that decision N5 selects. Check that each Deployment becomes ready and that the NMO operand has no registration.
7. Send a direct AdmissionReview to the replica that is not the leader.
8. Send dry-run requests during a replacement of the WAO Pods.
9. Check that the FAR and NHC dry-run requests of their documents still give the recorded answers.
10. Uninstall. Check that the registration is removed.
11. Run `make bundle-reset`.

Limit: rule 2, rule 4 and the rejection of rule 3 need a stored object or an etcd member that is not healthy.
They get envtest evidence only. Section 4, risk R5.

## 4. Risks, decisions and questions

### Risks

**R1. New read permissions for the WAO ServiceAccount.** See decision N1.

**R2. Behavior while NMO is `Disabled`.**
Before this work, a `NodeMaintenance` `CREATE` got no validation while NMO was `Disabled`. After it, the three rules apply.
An accepted object stays without effect until NMO is `Enabled`. Then the NMO controller cordons and drains the node. This was true before the work, and `retainedResources` reports the object.
The NMO controller adds a finalizer. A delete of an object with the finalizer does not complete while NMO is `Disabled`. Admission does not cause this.

**R3. Unused listener in the NMO operand.** The NMO Pod serves a webhook that no registration uses. It keeps its Service and its certificate. Same as [issue 12](https://github.com/jwmatthews/workload-availability-operator/issues/12) for NHC.

**R4. TLS profile.** Standalone NMO applies the TLS profile of the OpenShift API server to its webhook listener. The WAO listener does not.
This applies to all operands with central admission. [Issue 18](https://github.com/jwmatthews/workload-availability-operator/issues/18) is the tracker. This plan does not change the listener.

**R5. Live cluster tests are limited.** `AGENTS.md` forbids a real `NodeMaintenance`. Dry-run covers `CREATE` with rule 1, a pass, and the pass path of rule 3.

**R6. Stale registration after an update of WAO.** Same as [issue 11](https://github.com/jwmatthews/workload-availability-operator/issues/11). A cluster that had NMO `Enabled` on a previous build keeps `node-maintenance-operator-validating-webhook-configuration`. The acceptance procedure starts from a clean cluster.

**R7. Quorum rule on a cluster with no etcd PodDisruptionBudget.** On OpenShift, the handler rejects a control plane node when `openshift-etcd` has no PodDisruptionBudget, or when a list fails for a missing permission. Standalone NMO does the same. A hosted control plane cluster has no control plane nodes, so rule 3 does not apply there.

**R8. Base branch.** `feat/webhooks/nhc` does not have commit `df11d28` of `feat/webhooks/far`. This plan does not rebase. A rebase of the NMO branch follows a rebase of the NHC branch.

### Decisions

**N1 (2026-11-13, security). Scope and verbs of the new permissions.** [Issue 19](https://github.com/jwmatthews/workload-availability-operator/issues/19) is the tracker.

- The user gave the direction: stay as near to standalone NMO as possible.
- Standalone NMO has one ClusterRole and no namespaced Role. It has `get`, `list`, `watch`, `patch`, `update` on `nodes`, `get`, `list`, `watch` on `pods`, `create` on `pods/eviction`, and `get`, `list`, `watch` on `poddisruptionbudgets`.
- Decision: ClusterRole rules from markers, with the minimum verbs. `get` on `nodes`, `list` on `pods`, `list` on `poddisruptionbudgets`.
  - The scope is the same as the scope of NMO.
  - The verbs are only those that the WAO handler calls. The WAO handler has no cache, so `watch` is not necessary.
  - WAO does not get the NMO write verbs. Admission does not write, and those verbs permit a cordon and a drain of a node.
- Concerns:
  - The WAO ServiceAccount can list the Pods of each namespace. A Pod object can contain environment values and the names of Secrets. It does not contain Secret data.
  - The handler reads only `openshift-etcd`. The permission is larger than the use.
  - A missing permission rejects each `NodeMaintenance` for a control plane node. The user sees the error of the list.
- Not selected:
  - The NMO read verbs (`get`, `list`, `watch` on the three resources). WAO does not use the added verbs.
  - A Role and a RoleBinding in `openshift-etcd`. It is the minimum permission, but NMO does not have this structure. Not checked: if OLM installs a bundle Role into a namespace that is not the install namespace.
- Control: an offline test fails if the WAO role has a different verb on these three resources.

**N2. OpenShift check at start.** WAO imports `pkg/utils` of NMO and calls `NewOpenshiftValidator`, same as the NMO `cmd/main.go`.
Not selected: use of the NHC `Capabilities` value. It gives the same answer now, but it joins the NMO rule to a copied NHC file.

**N3. No `AdmissionBlock` for NMO.** No rule rejects a delete, and the one `UPDATE` rule does not depend on the controller. Section 2, row 5.

**N4. Keep the NMO webhook Service and certificate patch.** The unchanged NMO image stops without certificate files. [Issue 20](https://github.com/jwmatthews/workload-availability-operator/issues/20) records the unused listener.

**N5 (2026-11-13). Components to enable during cluster acceptance.**

- The test enables all six components with `hack/enable_all.sh`, checks the Deployments, then disables all. This checks that the operands install together with the new WAO build.
- Effect: SNR starts its DaemonSet on each node. No policy and no remediation request exists, so no remediation is expected.
- Not selected: enable NMO only.

**N6 (2026-11-13). Commits and push.** The work makes local commits. The user reviews the branch and does the push to the fork.

### Security record

Each row is a choice that changes what a person or a ServiceAccount can do or see. `docs/nmo-webhooks.md` gets the same table, with the results of the implementation.
A new security choice during the implementation gets a row here, a decision identifier and an issue.

| Choice | Selected | Concern that stays | Tracker |
| --- | --- | --- | --- |
| Permissions of the WAO ServiceAccount | Cluster scope, minimum verbs, no write verb (N1) | WAO can list the Pods of each namespace | [19](https://github.com/jwmatthews/workload-availability-operator/issues/19) |
| `failurePolicy` | `Fail`, unchanged from NMO | A `NodeMaintenance` `CREATE` or `UPDATE` fails when no WAO replica answers. Two replicas decrease the probability. | `AGENTS.md` |
| Answer when a read fails | Reject, unchanged from NMO | A failed read of a Node, the list or the etcd objects rejects the request. The handler does not pass a request that it cannot check. | None |
| Source of the handler data | Direct read from the API server, not a cache | Each `CREATE` adds 2 to 5 read requests to the API server. A user with `create` on `nodemaintenances` can cause these requests, also with dry-run. | [21](https://github.com/jwmatthews/workload-availability-operator/issues/21) |
| Information in the admission answer | Unchanged from NMO | A user with `create` on `nodemaintenances` learns if a node name exists and if a drain can break the etcd quorum, without `get` on nodes. Standalone NMO gives the same answer. With WAO, the answer is available also while NMO is `Disabled`. | [21](https://github.com/jwmatthews/workload-availability-operator/issues/21) |
| Log content | Unchanged from NMO | Object names and node names go to the WAO manager log. No Secret value. | None |
| TLS settings of the listener | No change in this plan | The WAO listener does not apply the API server TLS profile. Standalone NMO does. | [18](https://github.com/jwmatthews/workload-availability-operator/issues/18) |
| NMO operand listener | Stays, with its Service and certificate (N4) | One network listener and one certificate Secret have no use. | [20](https://github.com/jwmatthews/workload-availability-operator/issues/20) |
| OpenShift check at start | Stop the manager on a discovery error (N2) | A discovery error stops WAO admission for all operands. The alternative, an assumed answer, can disable the quorum rule. | [13](https://github.com/jwmatthews/workload-availability-operator/issues/13) |
| Registration owner | OLM, from the WAO CSV | A person with write access to the admission configuration can remove the entry. This is the same for each installer. | None |

### Issues of this work

| Subject | Source |
| --- | --- |
| [18](https://github.com/jwmatthews/workload-availability-operator/issues/18): TLS profile of the WAO admission listener | R4. Open. |
| [19](https://github.com/jwmatthews/workload-availability-operator/issues/19): cluster-wide read permissions for NMO admission, decision N1 | R1. Open. |
| [21](https://github.com/jwmatthews/workload-availability-operator/issues/21): NMO admission answers show node and etcd state to a user with `create` on `nodemaintenances`, and each request causes direct reads | Security record. Open. |
| [20](https://github.com/jwmatthews/workload-availability-operator/issues/20): unused webhook listener of the NMO operand | R3, N4. Open. |
| [23](https://github.com/jwmatthews/workload-availability-operator/issues/23): NMO admission rules 2, 3 (rejection) and 4 have no live cluster evidence | R5. Open. |
| [22](https://github.com/jwmatthews/workload-availability-operator/issues/22): a `NodeMaintenance` that is accepted while NMO is `Disabled` starts a drain when NMO is `Enabled` | R2. Open. |

## 5. Implementation sequence

Each commit must pass `make check` and `make test-offline`. Run `make test` on a clean tree before a push. Run `make bundle-reset` after each change of bundle sources.
The marker of the copied file changes the bundle. Thus the copy and the registration are one commit, as in the NHC work.

| # | Commit | Check |
| --- | --- | --- |
| 1 | docs: add the plan for the NMO admission webhook in WAO | Review |
| 2 | Initialize NMO admission in each WAO replica: module pin, copied files, `SOURCE.md`, lint exclusion, `setup.go`, RBAC markers, scheme, `cmd/main.go`, `make manifests`, `make bundle-reset`, `setup_test.go`, changed entry counts | `make check`, `make test-offline`. The role diff has only the planned rules. 6 entries. The `SOURCE.md` diff command shows only the listed adaptations. |
| 3 | Test the NMO validation behavior that WAO preserves (`wao_behavior_test.go`) | `go test ./internal/webhooks/nmo/v1beta1/...` |
| 4 | Move NMO admission registration to the WAO installation: delete patch, `make operand-manifests`, `nmo.go`, changed controller, component, render and offline tests | `make check`, `make test-offline`, `make test`. `objects.yaml` for NMO has no registration. |
| 5 | Test the registered NMO handler through an API server (`admission_test.go`) | `make test`, `make test-envtest ENVTEST_K8S_VERSION=1.29` |
| 6 | Test the NMO admission wiring of the manager binary (`cmd/main_test.go`) | `go test ./cmd/...` with envtest assets |
| 7 | docs: add `docs/nmo-webhooks.md` and update the overview documents | Review |
| 8 | docs: add classic OLM acceptance results (section 3.9). Server-side dry-run only. | Results table in `docs/nmo-webhooks.md` |

Not in this plan: OLM v1 acceptance, SNR and SBR admission, a change of the listener TLS settings, a rebase.

## 6. Implementation record (2026-11-13)

The sequence of section 5 is complete. The branch is local.

| Plan commit | Result |
| --- | --- |
| 1 | `docs: add the plan for the NMO admission webhook in WAO` |
| 2 | `Initialize NMO admission in each WAO replica` |
| 3 | `Test the NMO validation behavior that WAO preserves` |
| 4 | `Move NMO admission registration to the WAO installation` |
| 5 | `Test the registered NMO handler through an API server` |
| 6 | `Test the NMO admission wiring of the manager binary` |
| 7 and 8 | `docs: add NMO admission operation notes and classic OLM acceptance results` |

Differences from the plan text:

- Section 3.2: the copied suite `webhook_suite_test.go` uses the webhook path of the copied NMO configuration (`config/operands/nmo/config/webhook`), as the NHC copy does. The plan named `config/webhook`.
- Section 3.7: the envtest suite has no step for a second replica. The NHC suite has none, and the FAR suite tests the shared listener. The cluster acceptance sent requests to the replica that is not the leader.
- Section 3.7: `test/offline` has a shared function `checkCSVDefinition` for NHC and NMO. The lint check rejected the duplicate code.
- controller-gen puts `nodes` and `secrets` into one rule, because the two have the verb `get`. The test `TestWAOServiceAccountSecretPermissionIsGetOnly` now accepts a rule with other resources if the only verb is `get`.
- `go mod tidy` changed only the NMO module line in `go.mod`. No other requirement changed.
- Section 3.9, step 1: the cluster had no WAO installation at the start, so no removal was necessary.

Answers to the unknowns:

- The Go module proxy resolved the NMO revision as `v5.0.0-20261013171824-5baa709167a2`.
- After an uninstall with all components `Disabled`, the namespace had no SNR DaemonSet.

Issues for the concerns:

| Issue | Subject |
| --- | --- |
| [18](https://github.com/jwmatthews/workload-availability-operator/issues/18) | The WAO admission listener does not apply the API server TLS profile (R4) |
| [19](https://github.com/jwmatthews/workload-availability-operator/issues/19) | Cluster-wide read permissions for NMO admission (N1) |
| [20](https://github.com/jwmatthews/workload-availability-operator/issues/20) | Unused webhook listener of the NMO operand (R3, N4) |
| [21](https://github.com/jwmatthews/workload-availability-operator/issues/21) | Admission answers show node and etcd state, and each request causes direct reads |
| [22](https://github.com/jwmatthews/workload-availability-operator/issues/22) | A `NodeMaintenance` that is accepted while NMO is `Disabled` starts a drain when NMO is `Enabled` (R2) |
| [23](https://github.com/jwmatthews/workload-availability-operator/issues/23) | No live cluster evidence for rules 2 and 4 and the quorum rejection (R5) |
