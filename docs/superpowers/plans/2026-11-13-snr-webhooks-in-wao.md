# Plan: SNR admission webhooks in the WAO manager

Date: 2026-11-13. Branch: `feat/webhooks/snr`, from `feat/webhooks/nmo` at `ee1504f`.

This file is the plan. The implementation follows section 7.
The goal is that the WAO manager serves the Self Node Remediation (SNR) admission webhooks, with the same structure as the FAR, NHC and NMO work.
[The NMO plan](2026-11-13-nmo-webhooks-in-wao.md) is the template for the structure of this plan.

Terms in this plan:

- **Registration**: the `ValidatingWebhookConfiguration` or `MutatingWebhookConfiguration` entry that tells the API server to call a webhook.
- **Central admission**: admission that the WAO manager serves, with a registration that OLM installs from the WAO CSV.
- **Agent**: a Pod of the SNR DaemonSet. The SNR manager creates the DaemonSet from the `SelfNodeRemediationConfig`.
- **Singleton rule**: the SNR rule that permits only one `SelfNodeRemediationConfig`, with a fixed name, in the deployment namespace.

Decision identifiers in this plan have the prefix `S` (S1, S2, ...). NHC uses D1 to D4, and NMO uses N1 to N6.
John made the decisions on 2026-11-14. Section 6 has the answers.

## 1. SNR webhooks upstream

### Source pin

| Item | Value |
| --- | --- |
| Repository | <https://github.com/medik8s/self-node-remediation> |
| Local checkout | `../self-node-remediation` (read-only for this task) |
| Branch | `wao`. The local branch and `upstream/wao` have the same revision. |
| Revision | `96a9372cda44c0062fe1d2b28d79685557ab6502` (`v0.13.0-111-g96a9372c`) |
| Go module | `github.com/medik8s/self-node-remediation/v5` |
| Module version | `v5.0.0-20261102205523-96a9372cda44`. The Go module proxy resolved it on 2026-11-13. |
| SNR `go.mod` | Go 1.26.0, toolchain 1.26.5, controller-runtime v0.24.1, `k8s.io/api` v0.36.2, `medik8s/common` v1.17.0 |

WAO has the same versions or newer patch versions. No separate dependency update is expected.
`config/operands/snr/config/` is equal to the upstream `config/`, except for the image tag in `manager/kustomization.yaml`.

### Webhook inventory

SNR has four entries: one mutating and three validating. It has no conversion webhook. The API has one version, `v1alpha1`.
All three kinds are namespaced. The group is `self-node-remediation.medik8s.io`.
Source of the table: the markers, `config/webhook/manifests.yaml` and the `webhookdefinitions` of the SNR bundle CSV. The three sources agree.

| Entry name | Type | Kind and resource | Path | Operations | `failurePolicy` |
| --- | --- | --- | --- | --- | --- |
| `mselfnoderemediationtemplate.kb.io` | Mutating | `SelfNodeRemediationTemplate`, `selfnoderemediationtemplates` | `/mutate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate` | `CREATE`, `UPDATE` | `Fail` |
| `vselfnoderemediationtemplate.kb.io` | Validating | `SelfNodeRemediationTemplate`, `selfnoderemediationtemplates` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate` | `CREATE`, `UPDATE` | `Fail` |
| `vselfnoderemediationconfig.kb.io` | Validating | `SelfNodeRemediationConfig`, `selfnoderemediationconfigs` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig` | `CREATE`, `UPDATE`, `DELETE` | `Fail` |
| `vselfnoderemediation.kb.io` | Validating | `SelfNodeRemediation`, `selfnoderemediations` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation` | `CREATE`, `UPDATE` | **`Ignore`** |

For all four entries:

- `sideEffects` is `None`. The AdmissionReview version is `v1`.
- `timeoutSeconds` is not set. The API server default of 10 seconds applies.
  `config/webhook/kustomization.yaml` and `config/default/kustomization.yaml` of SNR have no patch for a webhook entry. This is different from NMO.
- No entry contains a status subresource.
- The Service is `webhook-service`, port 443 to 9443.

### Upstream files

| File | Content |
| --- | --- |
| `internal/webhook/v1alpha1/selfnoderemediationconfig_webhook.go` (314 lines) | `SetupSNRConfigWebhookWithManager(mgr)`, the marker, `SNRConfigValidator` and its rules |
| `internal/webhook/v1alpha1/selfnoderemediationtemplate_webhook.go` (93 lines) | `SetupSNRTemplateWebhookWithManager(mgr)`, two markers, `SNRTemplateDefaulter`, `SNRTemplateValidator`, `validateStrategy` |
| `internal/webhook/v1alpha1/selfnoderemediation_webhook.go` (62 lines) | `SetupSNRWebhookWithManager(mgr)`, the marker, `SNRValidator` |
| `internal/webhook/v1alpha1/*_webhook_test.go` (527, 91 and 83 lines) | Tests through an envtest API server |
| `internal/webhook/v1alpha1/webhook_suite_test.go` (143 lines) | envtest suite |
| `internal/utils/namespaces.go` | `GetDeploymentNamespace`. It reads the environment variable `DEPLOYMENT_NAMESPACE`. |
| `internal/utils/taints.go` | `IsOutOfServiceTaintSupported` (a package variable) and `InitOutOfServiceTaintFlagsWithRetry` |
| `api/v1alpha1` | Types, `ConfigCRName` (`self-node-remediation-config`). Public and importable. It imports only `medik8s/common/pkg/labels` and Kubernetes packages. |
| `cmd/main.go` | Line 249 calls `InitOutOfServiceTaintFlagsWithRetry`. Lines 300 to 313 register the three webhooks, only with `--is-manager`. There is no flag that disables the webhooks. |

Go does not let WAO import `internal/webhook/v1alpha1` and `internal/utils`.

### Rules

`SelfNodeRemediationConfig`:

1. `CREATE` and `UPDATE`: six duration fields have a minimum value. `maxTimeForNoPeersResponse` has a minimum only when it is set.
2. `CREATE` and `UPDATE`: each custom toleration and each custom node selector term of the DaemonSet must be valid.
3. `CREATE` and `UPDATE`: a warning, not a rejection, when `peerRequestTimeout` is less than `apiServerTimeout` plus 2 seconds.
4. `CREATE` only (singleton rule): the name must be `self-node-remediation-config`. The namespace must be the value of `DEPLOYMENT_NAMESPACE`. If the variable is not set, the handler rejects the request.
5. `DELETE`: a warning when the object is the default config in the deployment namespace. If `DEPLOYMENT_NAMESPACE` is not set, the handler rejects the delete of an object with the default name. No other rule rejects a delete.

`SelfNodeRemediationTemplate`:

6. Mutating, `CREATE` and `UPDATE`: add the annotation `remediation.medik8s.io/multiple-templates-support: "true"` when the annotation is absent.
7. Validating, `CREATE` and `UPDATE`: reject the strategy `OutOfServiceTaint` when `IsOutOfServiceTaintSupported` is false (Kubernetes older than 1.26).

`SelfNodeRemediation`:

8. Validating, `CREATE` and `UPDATE`: rule 7 for `spec.remediationStrategy`.

### Dependencies of the webhooks

| Dependency | Use | Access to the cluster |
| --- | --- | --- |
| Client | None. No handler reads an object from the cluster. | None |
| `DEPLOYMENT_NAMESPACE` | Rule 4 and rule 5 | None. The SNR Deployment sets it from `metadata.namespace`. |
| `IsOutOfServiceTaintSupported` | Rule 7 and rule 8 | One read of the server version (API discovery) at manager start, with a retry for 10 seconds. If it fails, SNR writes a log line and continues with the value false. |
| Feature gates, platform checks | None | None |
| RBAC | None for admission | None |

The handlers read no Secret. They write object names, durations and rejection text to the log.

## 2. The FAR, NHC and NMO pattern

Source: `git log main..HEAD` (39 commits), the three documents `docs/*-webhooks.md`, and the three plans in this directory.

| Subject | Pattern |
| --- | --- |
| Module | `go.mod` requires the operand module at a pseudo-version of the `wao` branch. No `replace` directive. `go mod vendor` is necessary after a change. |
| Copied files | `internal/webhooks/<key>/<version>/` has copies of the upstream `internal/webhook/<version>/` files and tests. License headers stay. Markers and rule bodies stay. `.golangci.yml` excludes the directory. |
| Other internal files | NHC copied two more internal packages (`annotations`, `cluster`) into `internal/webhooks/nhc/`. Public packages are imported, not copied. |
| Source record | `internal/webhooks/<key>/SOURCE.md`: pin, imported packages, copied files, each adaptation, a `diff` command. |
| Setup package | `internal/webhooks/<key>/setup.go`: `Options`, `SetupWithManager(mgr, opts)`, `Initialize(mgr, ...)`. RBAC markers for admission are in this file. |
| Shared code | `internal/webhooks/listener`: `NewAdmissionClient` (no cache), `AddReadyCheck` (readiness check `admission`), `Ready(mgr)`. |
| Registration point | `cmd/main.go` adds the API to the scheme and calls `Initialize` for each operand before `listener.AddReadyCheck`. The call is not behind leader election and not behind the operand state. A failure stops the manager. |
| Registration manifest | `make manifests` writes `config/webhook/manifests.yaml` from all markers. `config/webhook/kustomization.yaml` holds patches that a marker cannot hold (NMO timeout). |
| Certificates and OLM | Only `config/manifests` uses `config/webhook/`. operator-sdk converts the entries into `spec.webhookdefinitions` of the bundle CSV (`make bundle-reset`). OLM creates the Service, the certificate and the registrations. WAO has no certificate logic. |
| Operand manifests | `config/operands/<key>/overlays/runtime/webhook_configurations_delete_patch.yaml` deletes the operand registrations with `$patch: delete`. The CA injection patches are removed. `make operand-manifests` writes `internal/operands/manifests/<key>/objects.yaml`. |
| Operand listener | The unchanged operand image still starts a webhook listener. Thus the operand keeps `webhook_cert_patch.yaml` and `webhook_service_patch.yaml` (NHC issue 12, NMO issue 20). |
| Ownership | The registration is part of the WAO installation. It has no `WorkloadAvailabilityConfig` owner reference. WAO does not apply, adopt or delete it. Enable, disable and deletion of the `WorkloadAvailabilityConfig` do not change it. |
| Component definition | `internal/components/<key>.go`: remove `Webhooks`, set `CentralAdmission: true`. NHC also has an `AdmissionBlock`, because a rule rejects a delete that depends on controller state. |
| Controller | For `CentralAdmission`, `admissionReady` in the status comes from the local listener, in both states. No new controller code since NHC. |
| RBAC | Markers give the WAO ClusterRole only the verbs that a handler calls. An offline test pins the verbs. |
| Tests | Copied upstream tests. `wao_behavior_test.go`. `setup_test.go`. `admission_test.go` (envtest, real registration, user with only the generated ClusterRole). `cmd/main_test.go` (built binary). `test/offline/<key>_admission_test.go` (marker equals CSV, target Deployment, no operand registration, permissions). |
| Documents | `docs/<key>-webhooks.md`, and updates of `docs/Overview.md`, `docs/operand-config-overlays.md` and `README.md`. The plan gets an implementation record. |
| Acceptance | Classic OLM on a disposable cluster with `make dev-olm-deploy`. Server-side dry-run only. Checks in the states `Disabled`, `Enabled`, `Disabled` again, and after deletion of the `WorkloadAvailabilityConfig`. A request to the replica that is not the leader. Requests during a replacement of the WAO Pods. Uninstall. |
| Purge scripts | `hack/wao-purge.sh` and `hack/olmv1-purge-wao.sh` changed only for NHC (status clear before a delete). |
| Concerns | Each security-related choice has a row in a security record and a GitHub issue. |

Lesson from NMO: compare the upstream kustomize patches of the webhook entries with the WAO registration. For SNR there is no such patch (section 1).

## 3. Steps for SNR

### 3.1 What WAO can use again without a change

- The webhook server, the certificate path, 2 replicas, `config/webhook/service.yaml` and `kustomizeconfig.yaml`, the Makefile bundle steps.
- `listener.AddReadyCheck` and `listener.Ready`.
- The `CentralAdmission` field and the controller status logic.
- The mutating registration object in `config/webhook/manifests.yaml`. FAR already has two mutating entries.
- The environment variable `OPERATOR_NAMESPACE` of the WAO Deployment (decision S2).

SNR admission needs no admission client, no RBAC rule, no init container and no `AdmissionBlock`.

### 3.2 Source and copies

1. Add the SNR module at `v5.0.0-20261102205523-96a9372cda44` to `go.mod`. Run `go mod tidy` and `go mod vendor`. Review the `go.mod` diff.
2. Import `api/v1alpha1` from the module. Do not copy it.
3. Copy these files and keep the license headers:

| Upstream file | WAO destination | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1alpha1/selfnoderemediationconfig_webhook.go` | `internal/webhooks/snr/v1alpha1/` | Import path of `utils` only |
| `internal/webhook/v1alpha1/selfnoderemediationtemplate_webhook.go` | same | Import path of `utils` only |
| `internal/webhook/v1alpha1/selfnoderemediation_webhook.go` | same | None |
| The three `*_webhook_test.go` files | same | Import path of `utils`. The config test sets `OPERATOR_NAMESPACE`, not `DEPLOYMENT_NAMESPACE` (decision S2). |
| `internal/webhook/v1alpha1/webhook_suite_test.go` | same | CRD path `config/operands/snr/config/crd/bases`, webhook path `config/operands/snr/config/webhook`, a missing CRD path is an error |
| `internal/utils/namespaces.go` | `internal/webhooks/snr/utils/` | The variable name is `OPERATOR_NAMESPACE`, not `DEPLOYMENT_NAMESPACE` (decision S2) |
| `internal/utils/taints.go` | `internal/webhooks/snr/utils/` | None (decision S4) |

4. Write `internal/webhooks/snr/SOURCE.md` with the pin, the table and a `diff` command for both directories.
5. Add `internal/webhooks/snr/v1alpha1` and `internal/webhooks/snr/utils` to the lint exclusions in `.golangci.yml`.

The setup functions keep their upstream signatures, because no handler uses a client. This is different from FAR, NHC and NMO.

### 3.3 Setup package `internal/webhooks/snr`

- `setup.go` with `SetupWithManager(mgr ctrl.Manager) error` and `Initialize(mgr ctrl.Manager) error`. No `Options` type is necessary.
- `Initialize` does these steps. A failure of a step stops the manager, also when SNR is `Disabled`:
  1. Call `utils.GetDeploymentNamespace()`. An error stops the manager (decision S2).
  2. Call `utils.InitOutOfServiceTaintFlagsWithRetry(ctx, mgr.GetConfig())`. An error stops the manager (decision S3).
  3. Register the three webhooks in the order of the SNR `cmd/main.go`: config, template, remediation.
- No RBAC marker.
- `cmd/main.go`: add the SNR API to the scheme. Call `snrwebhooks.Initialize(mgr)` after the NMO call and before `listener.AddReadyCheck`.
- The WAO Deployment needs no change. It already sets `OPERATOR_NAMESPACE` from `metadata.namespace`, and `cmd/main.go` already stops when the value is empty.

### 3.4 Registration

1. Run `make manifests`. `config/webhook/manifests.yaml` gets one more mutating entry and three more validating entries. The total is 10 entries. `config/rbac/role.yaml` must not change.
2. Run `make bundle-reset`. The CSV gets four more `webhookdefinitions` entries with the WAO Deployment name.
3. Check that `vselfnoderemediation.kb.io` has `failurePolicy: Ignore` and that the other three entries have `Fail` (decision S1).
4. Check that no SNR entry has `timeoutSeconds` in `config/webhook/`. `config/webhook/kustomization.yaml` needs no SNR patch.

### 3.5 Operand manifests

1. Add `config/operands/snr/overlays/runtime/webhook_configurations_delete_patch.yaml` with `$patch: delete` for these two objects:
   - `self-node-remediation-validating-webhook-configuration`
   - `self-node-remediation-mutating-webhook-configuration`
2. Remove `webhook_validating_patch.yaml` and `webhook_mutating_patch.yaml` from the overlay and from its `kustomization.yaml`. Update the header comment.
3. Keep `webhook_cert_patch.yaml`, `webhook_service_patch.yaml` and `manager_image_env_patch.yaml` (decision S5).
4. Run `make operand-manifests`. Do not edit `internal/operands/manifests/snr/objects.yaml` directly.
5. Do not edit `config/operands/snr/config/`.

### 3.6 Component definition

- `internal/components/snr.go`: remove `Webhooks`, add `CentralAdmission: true`. Keep `AgentOwners` and `ManagedKinds`. No `AdmissionBlock` (decision S6).
- Result in the status of `selfNodeRemediation`: `admissionReady` shows the local listener in both states.
  The message `admission for this component's APIs is unavailable while Disabled` no longer applies to SNR.

### 3.7 Tests

| Test | Change |
| --- | --- |
| `internal/webhooks/snr/v1alpha1/` | Copied upstream tests. Add `wao_behavior_test.go` for these paths: rule 4 with the WAO namespace and with a different namespace, rule 4 and rule 5 without `OPERATOR_NAMESPACE`, the warning of rule 5, rule 6 with an existing annotation value, rule 7 and rule 8 with both flag values. |
| `internal/webhooks/snr/setup_test.go` | Nil manager, missing `OPERATOR_NAMESPACE`, server version error at start (stub API server), registration error |
| `internal/webhooks/snr/admission_test.go` | Through an envtest API server with the real registration and a user that has only the generated ClusterRole: a valid `CREATE` of each kind, the rejections of rules 1, 2 and 4, the warnings of rules 3 and 5, the annotation of rule 6 on the stored object, a status update gets no validation. With no listener: `SelfNodeRemediation` `CREATE` passes (`Ignore`), and the requests of the other three entries fail (`Fail`). |
| `cmd/main_test.go` | One SNR admission request to the built binary |
| `test/offline/snr_admission_test.go` | Each marker equals its CSV definition. The definitions point at the WAO Deployment. The `failurePolicy` of each CSV entry equals the entry in `config/operands/snr/config/webhook/manifests.yaml`. No registration in the SNR runtime objects. The Service and the certificate Secret name stay. The WAO role has no new rule. The WAO Deployment has `OPERATOR_NAMESPACE` from `metadata.namespace`. |
| `test/offline/far_admission_test.go`, `nhc_admission_test.go`, `nmo_admission_test.go` | Update each count of entries (6 to 10) and each list of operands that have no central admission (remove `snr`) |
| `internal/components/components_test.go` | SNR has 0 webhooks and `CentralAdmission` true |
| `internal/controller/workloadavailabilityconfig_controller_test.go` | Remove each expectation of the SNR registrations. Update the comment at line 555 about the SNR `Fail` webhooks. |
| `internal/operands/render_test.go` | Change it only if it expects an SNR registration |

### 3.8 Documents

- `docs/snr-webhooks.md` with the structure of `docs/nmo-webhooks.md`: entry table, ownership, startup, behavior that WAO preserves, differences from standalone SNR, the `Ignore` entry, the config and the agents while SNR is `Disabled`, security record, source pin update, dry-run procedure, acceptance results.
- Update `docs/operand-config-overlays.md`, `docs/Overview.md` and `README.md` where they list the operands with central admission.

### 3.9 Cluster acceptance

John named the cluster in the session. The kubeconfig path is not written in a committed file. Each command gets the kubeconfig explicitly.
All admission requests use server-side dry-run. The test creates no `SelfNodeRemediation`, no `SelfNodeRemediationConfig` and no policy.

1. Remove a previous WAO installation. Install with `make dev-olm-deploy`. Check that the six components are `Disabled` and that no operand Pod and no DaemonSet runs.
2. Check the 10 registrations: entry names, operations, `failurePolicy` (`Ignore` for one entry), the WAO Service, no owner reference.
3. Check that the permissions of the WAO ServiceAccount did not change.
4. Send these dry-run requests in each state (`Disabled`, `Enabled`, `Disabled` again, after deletion of the `WorkloadAvailabilityConfig`):
   - `SelfNodeRemediationConfig` `CREATE` with a different name: expect the rejection of rule 4.
   - `SelfNodeRemediationConfig` `CREATE` with the default name in a different namespace: expect the rejection of rule 4.
   - `SelfNodeRemediationConfig` `CREATE` with the default name in the WAO namespace and a duration that is too small: expect the rejection of rule 1.
   - `SelfNodeRemediationConfig` `CREATE`, valid, while no config exists: expect a pass.
   - `SelfNodeRemediationTemplate` `CREATE` with `-o yaml`: expect the annotation of rule 6 in the answer.
   - `SelfNodeRemediation` `CREATE`: expect a pass, and a `validate create` line in the WAO manager log.
   - `SelfNodeRemediationConfig` `DELETE` of the default config while SNR is `Enabled`: expect the warning of rule 5.
5. Enable all six components with `hack/enable_all.sh`. Check that the SNR manager creates its default config and its templates through WAO admission, and that the DaemonSet becomes ready.
6. Check that the SNR operand has no registration, and that its webhook Service and certificate Secret exist.
7. Send a direct AdmissionReview to the replica that is not the leader.
8. Send dry-run requests during a replacement of the WAO Pods.
9. Check that the FAR, NHC and NMO dry-run requests of their documents still give the recorded answers.
10. Disable all components. Expect the phase `DisableIncomplete` for SNR. Delete the `SelfNodeRemediationConfig` (a real delete). Check that the DaemonSet is removed and that the phase becomes `Disabled`.
11. Uninstall. Check that the 10 registrations are removed. Run `make bundle-reset`.

Limit: the rejection of rule 7 and rule 8 is not possible on a supported cluster. It gets test evidence only (risk R3).

## 4. Files likely to change

New files:

- `internal/webhooks/snr/SOURCE.md`, `setup.go`, `setup_test.go`, `admission_test.go`
- `internal/webhooks/snr/v1alpha1/`: three copied webhook files, three copied test files, `webhook_suite_test.go`, `wao_behavior_test.go`
- `internal/webhooks/snr/utils/namespaces.go`, `taints.go`
- `config/operands/snr/overlays/runtime/webhook_configurations_delete_patch.yaml`
- `test/offline/snr_admission_test.go`
- `docs/snr-webhooks.md`

Changed files:

- `go.mod`, `go.sum`, `.golangci.yml`
- `cmd/main.go`, `cmd/main_test.go`
- `config/webhook/manifests.yaml` (generated by `make manifests`)
- `bundle/manifests/workload-availability-operator.clusterserviceversion.yaml` (generated by `make bundle-reset`)
- `config/operands/snr/overlays/runtime/kustomization.yaml`
- `internal/operands/manifests/snr/objects.yaml` (generated by `make operand-manifests`)
- `internal/components/snr.go`, `internal/components/components_test.go`
- `internal/controller/workloadavailabilityconfig_controller_test.go`
- `test/offline/far_admission_test.go`, `nhc_admission_test.go`, `nmo_admission_test.go`
- `docs/Overview.md`, `docs/operand-config-overlays.md`, `README.md`

Deleted files:

- `config/operands/snr/overlays/runtime/webhook_validating_patch.yaml`, `webhook_mutating_patch.yaml`

Not expected to change: `config/manager/manager.yaml`, `config/rbac/role.yaml`, `config/webhook/kustomization.yaml`, `internal/webhooks/listener`, `internal/controller/*.go` (not tests), `Makefile`, the purge scripts, `config/operands/snr/config/`.

## 5. Risks and unknowns

The risks R1 to R6 are new with SNR. The prior three operands did not have them.

**R1. One entry has `failurePolicy: Ignore`.**
The six entries that WAO serves now all have `Fail`. `vselfnoderemediation.kb.io` has `Ignore` upstream.
`AGENTS.md` forbids a change of a component `failurePolicy`. Thus the WAO registration keeps `Ignore` (decision S1).
`Ignore` lets a `SelfNodeRemediation` request continue when no WAO replica answers. A remediation must not depend on the admission listener.
Effect on tests: the offline tests and the envtest suites must not assume `Fail` for each entry.

**R2. The config rules need the deployment namespace.**
Rule 4 and rule 5 read `DEPLOYMENT_NAMESPACE` in SNR. The WAO Deployment has `OPERATOR_NAMESPACE`, not `DEPLOYMENT_NAMESPACE`.
Without a value, each `CREATE` of a config fails, and the delete of the default config fails.
Then the SNR manager cannot create its default config at start, and a person cannot remove the agents.
WAO installs all operands in its own namespace, so the correct value is the WAO namespace.
Decision S2: the copied `namespaces.go` reads `OPERATOR_NAMESPACE`. The rejection text of a missing value names `OPERATOR_NAMESPACE`.

**R3. The strategy rule uses a package variable that is set at start.**

- The value comes from one read of the server version. Standalone SNR continues with false when the read fails. Then it rejects `OutOfServiceTaint`.
- The WAO CSV has `minKubeVersion: 1.29.0`. On each supported cluster the value is true, and rules 7 and 8 reject nothing.
- The read adds a wait of up to 10 seconds to the start of each WAO replica when the API server does not answer.
- Decision S3: stop the manager on an error, as NMO does for its OpenShift check.

**R4. The start of the SNR manager needs WAO admission.**
The SNR manager creates its default config and its templates at start. These requests go through the entries with `Fail`.
In standalone SNR, the same Pod serves the webhooks, and the template creator waits 10 seconds for its own listener.
With WAO, the requests fail when no WAO replica answers. The SNR manager process then stops, and the kubelet starts it again.
The singleton rule must accept the default config of the SNR manager. This depends on R2.

**R5. The remediation path calls the WAO listener.**
NHC creates a `SelfNodeRemediation`. The SNR controller updates it (two `Update` calls in `selfnoderemediation_controller.go`). Each request calls `vselfnoderemediation.kb.io`.
If the WAO Pods are on the failed nodes, each request can wait for the timeout of 10 seconds before `Ignore` applies.
Standalone SNR has the same wait when its one manager Pod is on the failed node. WAO has two replicas on two nodes.
Unknown: the time that the API server needs to stop the use of an endpoint on a failed node. No test of this plan measures it, because the acceptance creates no remediation.

**R6. The config, the agents and the `Disabled` state.**

- Before this work, a config request got no validation while SNR was `Disabled`. After it, rules 1 to 5 apply in both states.
- A person can create a valid default config while SNR is `Disabled`. No controller acts on it, and no DaemonSet starts.
  WAO reports the config in `retainedResources`, and the phase becomes `DisableIncomplete`. When SNR becomes `Enabled`, the SNR manager uses that config.
- After a disable, the config and the DaemonSet stay (`DisableIncomplete`). This is the documented WAO behavior.
  The person deletes the config. The delete now passes WAO admission with the warning of rule 5.
  The DaemonSet has a controller owner reference to the config (`selfnoderemediationconfig_controller.go` line 248). Unknown: if the garbage collector removes the DaemonSet with no SNR manager. Acceptance step 10 checks this.
- The agents do not serve a webhook. The webhooks are registered only with `--is-manager`.
- The warning text of rule 5 says "Self Node Remediation is now disabled". This is the SNR text, not the WAO component state.

**R7. Unused listener in the SNR operand.** The SNR manager always starts its webhook server. It looks for certificates in `/apiserver.local.config/certificates`, then in the controller-runtime default directory.
The server needs certificate files. Thus the operand keeps its Service and its certificate patch (decision S5). Same as issues 12 and 20.

**R8. Stale registrations after an update of WAO.** Same as issue 11. A cluster that had SNR `Enabled` on a previous build keeps the two legacy configurations. The legacy entries point at the SNR Service. The acceptance procedure starts from a clean cluster.

**R9. TLS profile.** Standalone SNR applies the TLS profile of the OpenShift API server to its webhook listener. The WAO listener does not. Issue 18 is the tracker.

**R10. Live cluster evidence is limited.** Dry-run covers `CREATE` for the three kinds and `DELETE` for the config. `UPDATE` rules and the rejection of rules 7 and 8 get envtest evidence only.

**R11. Effect of `go mod tidy`.** The SNR module requires packages that WAO does not use (`go-ping`, `grpc`). The imported package `api/v1alpha1` needs only `medik8s/common` and Kubernetes packages. Check the `go.mod` diff before the commit.

**R12. Copied `taints.go` contains code that WAO does not call** (`TaintExists`, `DeleteTaint`, the GA flag). A full copy keeps the `diff` command simple. Decision S4.

**R13. Base branch.** `feat/webhooks/snr` is on top of the NMO, NHC and FAR branches. A rebase of a lower branch needs a rebase of this branch. Each new operand changes the entry counts in the offline tests of the previous operands, so conflicts are probable there.

**R14. The copied `namespaces.go` and the copied config test are different from upstream.** Each update of the source pin must apply the variable name again. `SOURCE.md` lists the adaptation, and its `diff` command shows it.

### Decisions (2026-11-14)

| # | Decision | Not selected |
| --- | --- | --- |
| S1 | Keep `Ignore` for `vselfnoderemediation.kb.io` and `Fail` for the other three entries. An offline test compares each value with the copied SNR configuration. | `Fail` for all entries. `AGENTS.md` forbids the change, and it can block a remediation. |
| S2 | The copied `namespaces.go` reads `OPERATOR_NAMESPACE`, which the WAO Deployment already sets. The copied config test sets the same name. `Initialize` stops the manager when the variable is absent. | A second variable `DEPLOYMENT_NAMESPACE` in the WAO Deployment. A namespace field in the copied validators. An `os.Setenv` call in `cmd/main.go`. |
| S3 | Stop the manager when the server version read fails after the retry. | Continue with false, as standalone SNR does. Then WAO rejects each `OutOfServiceTaint` request until the Pod starts again. |
| S4 | Copy `taints.go` and `namespaces.go` as complete files. `namespaces.go` has the one adaptation of S2. | A copy of only the used functions. The `diff` command then shows many lines. |
| S5 | Keep the SNR webhook Service and certificate patch. | Removal. The unchanged SNR image needs the certificate files. |
| S6 | No `AdmissionBlock` for SNR. No rule rejects a delete or an update because of controller state. | An `AdmissionBlock`. No object qualifies. |

### Security record

| Choice | Selected | Concern that stays |
| --- | --- | --- |
| Permissions of the WAO ServiceAccount | No change | None |
| `failurePolicy` | Unchanged from SNR (S1) | Three kinds of request fail when no WAO replica answers. A `SelfNodeRemediation` request passes without validation in that condition. |
| Server version check at start | Stop the manager on an error (S3) | An API server error at start stops WAO admission for all operands |
| Information in the admission answer | Unchanged from SNR | A user with `create` on `selfnoderemediationconfigs` learns the WAO namespace from the rejection text of rule 4 |
| Log content | Unchanged from SNR | Object names and field values go to the WAO manager log. No Secret value. |
| TLS settings of the listener | No change in this plan | Issue 18 |
| SNR operand listener | Stays (S5) | One network listener and one certificate Secret have no use |
| Validation while SNR is `Disabled` | Rules apply in both states | A config that is accepted while `Disabled` starts the agents on each node when SNR becomes `Enabled` |

## 6. Answers from John (2026-11-14)

| # | Question | Answer |
| --- | --- | --- |
| Q1 | Keep `Ignore` for `vselfnoderemediation.kb.io` (S1) | Yes |
| Q2 | Source of the deployment namespace (S2) | The copied `namespaces.go` reads `OPERATOR_NAMESPACE` |
| Q3 | Stop the manager when the server version read fails (S3) | Yes |
| Q4 | Copy the two `internal/utils` files as complete files (S4) | Yes |
| Q5 | Components to enable during acceptance | All six, with `hack/enable_all.sh` |
| Q6 | Dry-run `CREATE` of a `SelfNodeRemediation`, and a real delete of the `SelfNodeRemediationConfig` that the SNR manager created | Permitted |
| Q7 | Location of this plan | `docs/superpowers/plans/`, as the first commit |
| Q8 | GitHub issues for the concerns | Yes, in the repository of John |
| Q9 | Commits and push | Local commits only. John reviews the branch and does the push. |
| Q10 | Cluster | John named the cluster in the session. The kubeconfig path is not written in a committed file. |

## 7. Implementation sequence

Each commit must pass `make check` and `make test-offline`. Run `make test` on a clean tree before a push. Run `make bundle-reset` after each change of bundle sources.
The markers of the copied files change the bundle. Thus the copy and the registration are one commit, as in the NHC and NMO work.

| # | Commit | Check |
| --- | --- | --- |
| 1 | docs: add the plan for the SNR admission webhooks in WAO | Review |
| 2 | Initialize SNR admission in each WAO replica: module pin, copied webhook files, copied `utils` files, `SOURCE.md`, lint exclusion, `setup.go`, scheme, `cmd/main.go`, `make manifests`, `make bundle-reset`, `setup_test.go`, `test/offline/snr_admission_test.go` (marker, CSV and `failurePolicy` checks), changed entry counts | `make check`, `make test-offline`. 10 entries. One entry with `Ignore`. `config/rbac/role.yaml` has no diff. The `SOURCE.md` diff command shows only the listed adaptations. |
| 3 | Test the SNR defaulting and validation behavior that WAO preserves (`wao_behavior_test.go`) | `go test ./internal/webhooks/snr/v1alpha1/...` |
| 4 | Move SNR admission registration to the WAO installation: delete patch, removal of the two CA injection patches, `make operand-manifests`, `snr.go`, changed controller, component and offline tests | `make check`, `make test-offline`, `make test`. `objects.yaml` for SNR has no registration. |
| 5 | Test the registered SNR handlers through an API server (`admission_test.go`) | `make test`, `make test-envtest ENVTEST_K8S_VERSION=1.29` |
| 6 | Test the SNR admission wiring of the manager binary (`cmd/main_test.go`) | `go test ./cmd/...` with envtest assets |
| 7 | docs: add `docs/snr-webhooks.md` and update the overview documents | Review |
| 8 | docs: add classic OLM acceptance results (section 3.9) and the implementation record. Server-side dry-run only. | Results table in `docs/snr-webhooks.md` |

Not in this plan: OLM v1 acceptance, SBR admission, a change of the listener TLS settings, a rebase, a push.

## 8. Implementation record (2026-11-14)

The sequence of section 7 is complete. The branch is local.

| Plan commit | Result |
| --- | --- |
| 1 | `docs: add the plan for the SNR admission webhooks in WAO` |
| 2 | `Initialize SNR admission in each WAO replica` |
| 3 | `Test the SNR defaulting and validation behavior that WAO preserves` |
| 4 | `Move SNR admission registration to the WAO installation` |
| 5 | `Test the registered SNR handlers through an API server` |
| 6 | `Test the SNR admission wiring of the manager binary` |
| 7 and 8 | `docs: add SNR admission operation notes and classic OLM acceptance results` |

Differences from the plan text:

- Section 3.7 did not list the envtest suite of NHC. That suite installs all registrations of `config/webhook/manifests.yaml` and serves only the NHC handler.
  Its duplicate-kind test used an SNR template, which now has a mutating entry with `Fail`. The test now uses the kind `MachineDeletionRemediationTemplate`, which has no admission webhook.
- The same test removed the annotation from an SNR template. In a WAO cluster the SNR defaulter adds the annotation again for each `UPDATE`. `docs/snr-webhooks.md` records this.
- Section 3.7 named a change of `test/offline/nmo_admission_test.go` and a removal of `snr` from a list in `far_admission_test.go`. The NMO file needed no change. The list in the FAR file checks a different subject and stays.
  The entry count is in `nhc_admission_test.go`. The FAR test now ignores the SNR definitions.
- Section 3.7 said that `internal/operands/render_test.go` changes only if it expects an SNR registration. It did: the render order test used the SNR admission configurations. The test now checks that SNR renders none.
- Section 3.5 named a delete patch with two documents. The patch has one document, and the kustomization applies it to the two kinds, as FAR does.
- `config/operands/snr/overlays/runtime/webhook_cert_patch.yaml` has a changed comment. It is not in section 4.
- `go mod tidy` changed only the SNR module line in `go.mod`. No other requirement changed.
- `make fix-imports` changed the import order of five copied files. The `diff` command of `SOURCE.md` shows it.
- The setup test for a failed version read needs 10 seconds, because the copied function tries again.
- After this work, no operand renders an admission configuration. The `Webhooks` field of the component definition has no user. The controller code for it stays.

Answers to the unknowns:

- R6: the garbage collector removed the DaemonSet after the delete of the config with no SNR manager. The config has no finalizer.
- R4: with two ready WAO replicas, the SNR manager created its config, its template and the DaemonSet. Each component became `Available` in 16 seconds.
- R5: not measured. The acceptance created no remediation.

Issues for the concerns:

| Issue | Subject |
| --- | --- |
| [24](https://github.com/jwmatthews/workload-availability-operator/issues/24) | The SNR remediation path calls the WAO listener through an entry with `Ignore` (R1, R5, S1) |
| [25](https://github.com/jwmatthews/workload-availability-operator/issues/25) | The SNR manager start and a config delete need a WAO replica (R4) |
| [26](https://github.com/jwmatthews/workload-availability-operator/issues/26) | A config that is accepted while SNR is `Disabled` starts the agents when SNR is `Enabled` (R6) |
| [27](https://github.com/jwmatthews/workload-availability-operator/issues/27) | Unused webhook listener of the SNR operand (R7, S5) |
| [28](https://github.com/jwmatthews/workload-availability-operator/issues/28) | SNR admission reads the namespace from `OPERATOR_NAMESPACE` (R2, R14, S2) |
| [29](https://github.com/jwmatthews/workload-availability-operator/issues/29) | SNR admission stops each WAO replica when the version read fails (R3, S3) |
| [30](https://github.com/jwmatthews/workload-availability-operator/issues/30) | No live cluster evidence for `UPDATE` through the API server, the strategy rejection and the `Ignore` behavior (R10) |
