# Plan: NHC admission webhooks in the WAO manager

Date: 2026-11-12. Branch: `feat/webhooks/nhc`, from `feat/webhooks/far` at `7b784ea`.

This file is the plan. The implementation follows section 5.
The goal is that the WAO manager serves the Node Healthcheck Operator (NHC) admission webhook, with the same structure as the Fence Agents Remediation (FAR) work.

Terms in this plan:

- **Registration**: the `ValidatingWebhookConfiguration` or `MutatingWebhookConfiguration` entry that tells the API server to call a webhook.
- **Central admission**: admission that the WAO manager serves, with a registration that OLM installs from the WAO CSV.
- **Capabilities**: the NHC struct `cluster.Capabilities`, which NHC fills one time at start from API discovery and the `Infrastructure` object.

## 1. NHC webhooks upstream

### Source pin

| Item | Value |
| --- | --- |
| Repository | <https://github.com/medik8s/node-healthcheck-operator> |
| Local checkout | `/home/jmatthews/git/jwmatthews/node-healthcheck-operator` (read-only for this task) |
| Branch | `wao` (same revision on `upstream/wao`) |
| Revision | `8b326d3acfe5cc44029057486a35e9ea594d25cb` (`v0.12.0-91-g8b326d3a`) |
| Go module | `github.com/medik8s/node-healthcheck-operator/v5` |
| Resolved module version | `v5.0.0-20261103000740-8b326d3acfe5` (the Go module proxy resolves it) |
| NHC `go.mod` | Go 1.26.0, controller-runtime v0.24.1, `k8s.io/api` v0.36.2, `medik8s/common` v1.17.0, `openshift/api` v0.0.0-20260805215214-cfb63858e9d7 |

WAO already uses Go 1.26.0 (toolchain 1.26.5), controller-runtime v0.24.1, `k8s.io/api` v0.36.3 and `medik8s/common` v1.17.0.
Thus no separate dependency update is necessary, unlike FAR Task 1. `openshift/api` is not in the WAO `go.mod` now.

### Webhook inventory

NHC has one webhook. It has no mutating webhook, no defaulter and no conversion webhook. The API has one version, `v1alpha1`.

| Item | Value |
| --- | --- |
| Type | Validating |
| Entry name | `vnodehealthcheck.kb.io` |
| Group, version, kind | `remediation.medik8s.io`, `v1alpha1`, `NodeHealthCheck` (cluster-scoped) |
| Resource | `nodehealthchecks` (not `nodehealthchecks/status`) |
| Path | `/validate-remediation-medik8s-io-v1alpha1-nodehealthcheck` |
| Operations | `CREATE`, `UPDATE`, `DELETE` |
| `failurePolicy` | `Fail` |
| `sideEffects` | `None` |
| AdmissionReview versions | `v1` |

### Upstream files

| File | Content |
| --- | --- |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook.go` (227 lines) | `SetupWebhookWithManager(mgr, caps)`, the `+kubebuilder:webhook` marker, `customValidator` and all validation logic |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook_test.go` (478 lines) | Unit tests with a mock client |
| `internal/webhook/v1alpha1/webhook_suite_test.go` (145 lines) | envtest suite |
| `internal/controller/cluster/capabilities.go` | `Capabilities` and `NewCapabilities` |
| `internal/controller/utils/annotations/annotations.go` | `HasMultipleTemplatesAnnotation` |
| `api/v1alpha1/validation.go` | `ValidateMinHealthyMaxUnhealthy` (public, importable) |
| `cmd/main.go` | Calls `webhookv1alpha1.SetupWebhookWithManager(mgr, &caps)` with no condition. There is no flag that disables the webhook. |
| `config/webhook/manifests.yaml`, `service.yaml` | Generated registration and Service (port 443 to 9443) |

### Validation rules

1. `minHealthy` and `maxUnhealthy`: exactly one, not negative, and `maxUnhealthy` not with `MachineDeletionRemediationTemplate`.
2. The selector is mandatory and must parse.
3. Exactly one of `remediationTemplate` and `escalatingRemediations`.
4. Escalating remediations: unique `order`, `timeout` of 1 minute or more, and no duplicate template kind. A duplicate kind is permitted only when each template of that kind in the cluster has the annotation `remediation.medik8s.io/multiple-templates-support`.
5. The control plane topology must be a supported one (`caps.IsSupportedControlPlaneTopology`).
6. `UPDATE`: while `status.unhealthyNodes[].remediations` is not empty, a change of the selector, the template or the escalating remediations is rejected.
7. `DELETE`: rejected while `status.unhealthyNodes[].remediations` is not empty.

### Dependencies of the webhook

| Dependency | Use | Access to the cluster |
| --- | --- | --- |
| `mgr.GetClient()` | Rule 4: lists all objects of the template kind, in all namespaces, as unstructured objects | `list` on each remediation template kind. NHC gets this permission from its aggregated ClusterRole `node-healthcheck-operator-aggregation`, which the NHC manager creates at start. |
| `cluster.Capabilities` | Rule 5 | Discovery of API groups. On OpenShift, `get` on `config.openshift.io/infrastructures` `cluster`. Read one time at manager start. |
| `annotations` (internal) | Rule 4 | None. Uses `medik8s/common/pkg/annotations`. |
| `api/v1alpha1` | Types and rule 1 | None |
| Stored object status | Rules 6 and 7 | None. The API server sends the object in the request. |

The webhook does not use feature gates, the cluster-version check, the upgrade checker or the MachineHealthCheck checker. Only the NHC controllers use those.
The webhook reads no Secret.
Go does not let WAO import the three `internal/` packages. The validation logic is in the internal webhook file. FAR was different: its validators are in the public `api/v1alpha1` package.

## 2. Structure of the FAR integration

Source: `git log main..feat/webhooks/far` (14 commits after the 2 from `main`), `docs/far-webhooks.md`, `docs/superpowers/plans/2026-11-11-far-webhooks-in-wao.md`.

### Code

| Location | Purpose |
| --- | --- |
| `go.mod` | Pins the FAR module at a pseudo-version of the `wao` branch. No `replace` directive. |
| `internal/webhooks/far/SOURCE.md` | Source pin, imported packages, copied files, adaptation of each file, and a `diff` command for a refresh |
| `internal/webhooks/far/v1alpha1/` | Copies of the two FAR webhook files and their tests. The one adaptation: the setup functions receive `admissionClient client.Client`. The markers are unchanged. |
| `internal/webhooks/far/setup.go` | `Options`, `SetupWithManager`, `Initialize`, `ReadyCheckName` (`far-admission`), `Ready(mgr)`. Holds the RBAC marker for Secrets. |
| `internal/webhooks/far/client.go` | `NewAdmissionClient`: a client that reads directly from the API server, without a cache |
| `internal/webhooks/far/inventory/` | FAR-specific list of fence agent names |
| `.golangci.yml` | Excludes the copied directory from lint |

### Registration points

- `cmd/main.go`: adds `farapi` to the scheme. Adds `--webhook-port` and uses `--webhook-cert-path` as `CertDir`. Calls `farwebhooks.Initialize(mgr, ...)` before the manager starts, not behind leader election and not behind the operand state. A failure stops the manager. Gives `farwebhooks.Ready(mgr)` to the reconciler as `FARAdmissionReady`.
- `internal/components/component.go`: new field `CentralAdmission`. `internal/components/far.go` sets it and has no `Webhooks` entry.
- `internal/controller/workloadavailabilityconfig_controller.go`: for a `CentralAdmission` component, `admissionReady` in the status comes from the local webhook listener, in the `Enabled` state and in the `Disabled` state.

### Manifests and certificates

- `make manifests` runs controller-gen with `webhook paths="./..."`. It writes `config/webhook/manifests.yaml` from the markers of the copied files.
- `config/webhook/` (`kustomization.yaml`, `kustomizeconfig.yaml`, `service.yaml`) is used only by `config/manifests`. operator-sdk converts the objects into `spec.webhookdefinitions` of the bundle CSV. The Makefile `bundle` target removes the generated Service file from the bundle.
- Classic OLM creates the Service, the serving certificate, the certificate mount and the registrations. OLM v1 on OpenShift uses service-ca. WAO has no certificate provider logic.
- `config/manager/manager.yaml`: 2 replicas, pod anti-affinity, container port 9443, `--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs`. The init container and the volume are FAR-specific.
- `config/rbac/role.yaml`: `get` on Secrets, generated from the marker.

### Removal from the operand manifests

- `config/operands/far/overlays/runtime/webhook_configurations_delete_patch.yaml` deletes the two FAR registrations with `$patch: delete`. The copied upstream directory is unchanged.
- The Service patch and the certificate patch stay, because the unchanged FAR image starts its webhook listener and needs the certificate files.
- `make operand-manifests` regenerated `internal/operands/manifests/far/objects.yaml` (102 lines removed).

### Ownership

- OLM owns the registration through the CSV. WAO does not apply, own, adopt or delete it. It has no `WorkloadAvailabilityConfig` owner reference.
- Enable, disable, and deletion of the `WorkloadAvailabilityConfig` do not remove the registration. An uninstall of WAO removes it.

### Tests

| Test location | What it checks |
| --- | --- |
| `internal/webhooks/far/v1alpha1/` | Copied FAR tests, plus `wao_behavior_test.go` for the behavior that WAO preserves |
| `internal/webhooks/far/` | `setup_test.go` (options, startup errors), `readiness_test.go`, `admission_test.go` (`TestAdmissionThroughAPIServer`: real registrations in envtest, second replica, `Fail` policy with no listener) |
| `cmd/main_test.go` | Starts the built manager binary with envtest |
| `test/offline/far_admission_test.go` | Markers equal CSV definitions, definitions point at the WAO Deployment, no registration in the FAR runtime objects, Secret permission is `get` only |
| `test/offline/manager_deployment_test.go` | Replicas, init container, security settings |
| `internal/components/components_test.go` | `TestFARAdmissionIsCentral`: webhook counts for each component |
| `internal/controller/*_test.go` | Status with central admission |
| `Makefile` | `ENVTEST_PACKAGES`, `test-envtest` for Kubernetes 1.29 and the race detector |
| `docs/far-webhooks.md` | Acceptance results on classic OLM and OLM v1, with server-side dry-run only |

## 3. Steps for NHC

### 3.1 What WAO can use again without a change

- The webhook server, port 9443, the certificate path, 2 replicas, the `config/webhook/` directory, the Makefile bundle steps and the envtest version matrix.
- The readiness of the local listener. NHC and FAR use the same listener.
- The `CentralAdmission` field and the controller status logic.

NHC needs no init container, no image inventory, no Secret permission and no dependency update commit.

### 3.2 Source and copies

1. Add `github.com/medik8s/node-healthcheck-operator/v5 v5.0.0-20261103000740-8b326d3acfe5` to `go.mod`. Run `go mod tidy`.
2. Import `api/v1alpha1` from the module. Do not copy it.
3. Copy these files and keep the license headers:

| Upstream file | WAO destination | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook.go` | `internal/webhooks/nhc/v1alpha1/` | Import paths of `cluster` and `annotations` point at the WAO copies. `SetupWebhookWithManager` receives `admissionClient client.Client` (same adaptation as FAR). Marker unchanged. |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook_test.go` | same | Import paths only |
| `internal/webhook/v1alpha1/webhook_suite_test.go` | same | CRD path `config/operands/nhc/config/crd/bases`, webhook path `config/webhook`, missing CRD path is an error, setup call |
| `internal/controller/cluster/capabilities.go` | `internal/webhooks/nhc/cluster/` | None. Do not copy `upgrade_checker.go`. |
| `internal/controller/utils/annotations/annotations.go` | `internal/webhooks/nhc/annotations/` | None |

4. Write `internal/webhooks/nhc/SOURCE.md` with the pin, the table above and the `diff` command.
5. Add `internal/webhooks/nhc/v1alpha1`, `cluster` and `annotations` to the lint exclusions in `.golangci.yml`.

### 3.3 Setup package `internal/webhooks/nhc`

- `setup.go` with `Options{AdmissionClient, Capabilities}`, `SetupWithManager` and `Initialize(mgr)`.
- `Initialize` does these steps. A failure of a step stops the manager, also when NHC is `Disabled`:
  1. Make the dedicated client.
  2. Call `cluster.NewCapabilities(mgr.GetConfig(), admissionClient, log, ctx)`. Use the dedicated client as the reader, because the manager cache has not started.
  3. Register the handler.
- RBAC markers in `setup.go`:
  - `config.openshift.io`, `infrastructures`, `get`.
  - `list` on the four template resources in the WAO bundle: `selfnoderemediationtemplates`, `fenceagentsremediationtemplates`, `storagebasedremediationtemplates`, `machinedeletionremediationtemplates`. See risk R1 and decision D2.
- Scheme in `cmd/main.go`: add the NHC API and `configv1` from `openshift/api`. The copied `capabilities.go` reads a typed `Infrastructure`.

### 3.4 Shared pieces that are now FAR-specific

Do this as one small refactor commit before the NHC code. It changes no behavior.

- Move `NewAdmissionClient`, `Ready` and the readiness check registration from `internal/webhooks/far` to a shared package, for example `internal/webhooks/listener`.
- Keep one readiness check for the listener. Rename it from `far-admission` to `admission` (decision D3). Update the FAR tests and `docs/far-webhooks.md` for the new name.
- Rename the reconciler field `FARAdmissionReady` to `CentralAdmissionReady`. Update `status_test.go` and `cmd/main.go`.
- `config/webhook/kustomization.yaml`, `service.yaml` and `config/manifests/kustomization.yaml` have comments that name only FAR. Update the comments.

### 3.5 Registration

1. Run `make manifests`. `config/webhook/manifests.yaml` gets a fifth entry, `vnodehealthcheck.kb.io`, in the validating configuration. `config/rbac/role.yaml` gets the new rules.
2. Run `make bundle-reset`. The CSV gets a fifth `webhookdefinitions` entry with `deploymentName: workload-availability-operator-controller-manager`.
3. Check that the entry has `failurePolicy: Fail` and the three operations.

### 3.6 Operand manifests

1. Add `config/operands/nhc/overlays/runtime/webhook_configurations_delete_patch.yaml` with `$patch: delete` for `node-healthcheck-validating-webhook-configuration`.
2. Remove `webhook_validating_patch.yaml` from the overlay and from its `kustomization.yaml`. Update the header comment.
3. Keep `webhook_cert_patch.yaml` and `webhook_service_patch.yaml`. The unchanged NHC image starts its webhook listener and stops without certificate files.
4. Run `make operand-manifests`. Do not edit `internal/operands/manifests/nhc/objects.yaml` directly.
5. Do not edit `config/operands/nhc/config/`.

### 3.7 Component definition and controller

- `internal/components/nhc.go`: remove `Webhooks`, add `CentralAdmission: true`.
- No controller logic change is necessary for admission readiness after step 3.4.
- Result in the status of `nodeHealthCheck`: `admissionReady` shows the local listener in both states. The message `admission for this component's APIs is unavailable while Disabled` goes away for NHC.

### 3.7a Report of blocked policies while NHC is `Disabled`

This step implements the decision for Q1 (section 4, decision D1).

Today `ensureDisabled` calls `retainedResources`, which lists all `NodeHealthCheck` objects. If the list is not empty, the phase is `DisableIncomplete`, the names go into `retainedResources`, and the message is general.
The message does not show which objects admission blocks.

1. Add a field to `components.Definition` that holds the rule, for example `BlockedByStatus`, next to `ManagedKinds`. Only NHC sets it. The reconciler function stays general.
2. In the `Disabled` state, the reconciler reads `status.unhealthyNodes[].remediations` of each listed `NodeHealthCheck`. The data is in the list result. No additional request and no new permission is necessary.
3. For each object with a remediation entry, the component message contains:
   - the name of the object,
   - that admission rejects its delete and a change of its selector, template or escalating remediations,
   - the two procedures: set the component to `Enabled` again, or remove `status.unhealthyNodes` with a patch to the status subresource.
4. The report is information only. It changes no admission answer and it changes no `NodeHealthCheck`.

Example:

```yaml
- name: nodeHealthCheck
  desiredState: Disabled
  phase: DisableIncomplete
  admissionReady: true
  retainedResources:
    - remediation.medik8s.io/v1alpha1 NodeHealthCheck worker-policy
  message: >-
    [manager removed; ... NodeHealthCheck worker-policy shows a remediation in its status.
    Admission rejects its delete and a change of its selector or templates.
    Enable the component again, or clear status.unhealthyNodes with a status patch.]
```

Coverage of the report:

| Path to a stale status | Does the report show it? |
| --- | --- |
| The user sets NHC to `Disabled` | Yes |
| A person deletes the NHC Deployment while NHC is `Enabled` | Not applicable. WAO makes the Deployment again and NHC continues. |
| The `WorkloadAvailabilityConfig` is deleted | No, until WAO makes the default config again at manager start |
| WAO is uninstalled | Not applicable. The registration is gone, so no rule blocks the user. |

### 3.8 Tests

| Test | Change |
| --- | --- |
| `internal/webhooks/nhc/v1alpha1/` | Copied upstream tests. Add `wao_behavior_test.go` for rules 4 to 7 with a fake client: list error, absent annotation, unsupported topology, update and delete during remediation. |
| `internal/webhooks/nhc/setup_test.go` | Nil options, registration error, startup error when the `Infrastructure` read fails, non-OpenShift discovery result |
| `internal/webhooks/nhc/admission_test.go` | Through an envtest API server with the real registration: valid `CREATE`, each rejection, `UPDATE` and `DELETE` with a status that shows a remediation, status subresource update is not validated, second replica, `Fail` with no listener. This is the only place that can test a real `DELETE`. |
| `cmd/main_test.go` | One NHC admission request to the built binary. The test API server needs the `Infrastructure` CRD only if the test simulates OpenShift. |
| `test/offline/far_admission_test.go` | The marker count changes from 4 to 5. Make the table cover both components, or add `nhc_admission_test.go` and move the shared helpers. Add: no registration in the NHC runtime objects, Service and certificate Secret name stay, WAO role has `get` on `infrastructures` and `list` only on templates. Add the test from decision D2: read each bundle ClusterRole with the label `rbac.ext-remediation/aggregate-to-ext-remediation`, and fail if the WAO role does not have `list` on each template resource in those roles. |
| `internal/components/components_test.go` | `TestFARAdmissionIsCentral`: NHC has 0 webhooks and `CentralAdmission` true |
| `internal/operands/render_test.go` | `TestRenderNHCReplacesNamespaceImagesAndLabels` reads the validating configuration at line 161. Remove that part. |
| `internal/controller/workloadavailabilityconfig_controller_test.go` | Lines 350, 372 and 511 expect the NHC registration and `admissionReady: false`. Change them. Add cases for step 3.7a: a `Disabled` NHC with a policy that shows a remediation (message names the object and the procedures), with a policy that shows none (no blocked text), and with no policy. |
| `Makefile` | Add `./internal/webhooks/nhc/...` to `ENVTEST_PACKAGES` and to the race detector run |

### 3.9 Documents

- `docs/nhc-webhooks.md` with the structure of `docs/far-webhooks.md`: entry table, ownership, startup, behavior that WAO preserves, contributor checks, source pin update, dry-run procedure, acceptance results.
- `docs/nhc-webhooks.md` also has a section for decision D2: the limit for a template kind that is not in the WAO bundle, the error text, and the procedure for a cluster administrator (a ClusterRole with `list` on the template resource, and a binding to the WAO ServiceAccount).
- `docs/nhc-webhooks.md` also has a section for decision D1: the blocked state, the two error texts, the status report, and the status patch procedure with a command example.
- `hack/wao-purge.sh` and `hack/olmv1-purge-wao.sh` must handle a rejected `NodeHealthCheck` delete. They clear `status.unhealthyNodes` with a status patch before the delete.
- Update `docs/operand-config-overlays.md`, `docs/Overview.md` and `README.md` where they list FAR as the only central admission.
- `hack/wao-purge.sh` and `hack/olmv1-purge-wao.sh` delete registrations by name. Check that they still remove all NHC entries.

## 4. Risks and unknowns

### Items that NHC needs and FAR did not

**R1. Template `list` permission for the WAO ServiceAccount.**
Rule 4 lists objects of any template kind. On a list error the upstream code logs the error and reports "multiple templates not supported".
Thus a missing permission does not fail the request openly. It rejects a `NodeHealthCheck` that has two templates of the same kind.
Standalone NHC gets the permission from an aggregated ClusterRole that the NHC manager creates. That role exists only while NHC runs.
Only the FAR and SNR template webhooks set the annotation. SBR and MDR do not, so a duplicate SBR or MDR kind is rejected with or without the permission.
[Issue 10](https://github.com/jwmatthews/workload-availability-operator/issues/10) is the tracker. Decision D2 selects option A.
Options:

- A (recommended for this proof of concept): static `list` rules for the four template kinds in the WAO bundle. A template kind from a different vendor, for example Metal3, gets the "not supported" answer.
- B: a bundle ClusterRole with an `aggregationRule` on `rbac.ext-remediation/aggregate-to-ext-remediation`, and a binding for the WAO ServiceAccount. This matches NHC, but it gives WAO write permissions that the remediator roles contain. The bundle check in `hack/verify-operand-manifests.sh` must permit the new objects.

**R2. Capabilities at start.**
The WAO manager must read `Infrastructure/cluster` before it serves. A read failure stops each WAO replica, also when NHC is `Disabled`.
The value is a snapshot from the Pod start, same as NHC.
On an unsupported topology, WAO rejects each `NodeHealthCheck` `CREATE` and `UPDATE`. Standalone NHC does the same.

**R3. New dependency `openshift/api`.**
The copied `capabilities.go` imports `github.com/openshift/api/config/v1`. WAO has no `openshift/api` requirement now.
Alternative: WAO-owned discovery code with an unstructured read. That removes the dependency, but the supported topology list is then not a copy and a refresh can miss a change.
Decision D4 selects the copy. Check the effect of `go mod tidy` on the Kubernetes module versions before the commit.

**R4. The `DELETE` operation with `failurePolicy: Fail`.**
FAR has no `DELETE` entry. For NHC, a `NodeHealthCheck` delete fails when no WAO replica answers.
Unknown: if a CRD deletion (`operator-sdk cleanup --delete-all`, the purge scripts) stops when `NodeHealthCheck` objects exist and the WAO Deployment is gone before the registration. Check this in envtest. Do not change the `failurePolicy`.

**R5. Rules 6 and 7 while NHC is `Disabled`.**
Today a `Disabled` NHC has no registration, so a user can change or delete a `NodeHealthCheck`.
With central admission, an object with `status.unhealthyNodes[].remediations` cannot be deleted and its selector and templates cannot change. No controller clears the status while NHC is `Disabled`.
The status subresource is not in the webhook rules, so a status patch can clear it.
[Issue 9](https://github.com/jwmatthews/workload-availability-operator/issues/9) is the tracker. Decision D1 below applies.

Facts from the NHC source (`internal/controller/resources/manager.go`):

- NHC adds no finalizer to the `NodeHealthCheck`. Without the webhook rule, a delete completes without the NHC controller.
- Each remediation request has an owner reference to the `NodeHealthCheck`.
- A remediation request gets a second owner reference to the Machine of the node, when the cluster has the Machine API and the Machine is in the namespace of the template. Kubernetes deletes a dependent only when all its owners are gone. In that case a delete of the policy does not remove the remediation requests. Issue 9 does not say this yet.

**R6. Live cluster tests are limited.**
`AGENTS.md` forbids a real `NodeHealthCheck` on a cluster. Server-side dry-run covers `CREATE` only.
`UPDATE`, `DELETE` and rules 6 and 7 have envtest evidence only. Say this in the acceptance results.

**R7. More copied logic than FAR.**
All NHC validation is in the copied file (227 lines) plus two copied packages. For FAR the copies were thin wrappers.
Each NHC source pin update must copy the files again and review the diff.

### Items in common with FAR

**R8. Stale registration after an update of WAO.** A cluster that has NHC `Enabled` on a previous WAO build has `node-healthcheck-validating-webhook-configuration` with a `WorkloadAvailabilityConfig` owner reference. Checked on 2026-11-13: the reconciler does not remove an object that it no longer renders. `ensureEnabled` applies the rendered objects only, and `ensureDisabled` deletes the rendered objects only. The stale registration stays until the `WorkloadAvailabilityConfig` is deleted (garbage collection through the owner reference). While it stays, the API server calls two validators for each request. The FAR work declared upgrade outside the release, and this plan does the same.

**R9. Standalone NHC together with WAO.** Not supported, same as FAR. WAO does not adopt or delete a registration of a different installer.

**R10. Unused listener in the NHC operand.** The NHC Pod still serves a webhook that no registration uses, and keeps its Service and certificate. NHC has no flag to stop the listener. The FAR follow-up (`--enable-webhooks=false`, issue 7) has no NHC equivalent yet.

**R11. Logs.** The validator writes the object name for each request to the WAO manager log. It writes no Secret value.

**R12. Kubernetes 1.29.** NHC builds against Kubernetes 1.36 modules. Run the NHC admission suite on envtest 1.29 and on the default version, same as FAR.

### Decisions

**D1 (2026-11-13, answers Q1, risk R5). Working decision: can change before the implementation.**

- WAO keeps the upstream `UPDATE` and `DELETE` rules in the `Enabled` state and in the `Disabled` state (option A of issue 9). The copied validation code has no WAO logic change.
- WAO does not block the change of NHC to `Disabled` while remediations are present. Reasons:
  - A remediation can stay for an unlimited time, because NHC keeps the request while the node is unhealthy.
  - The disable is the control that stops new remediation requests during a remediation storm.
  - A block does not cover an uninstall or a deletion of the `WorkloadAvailabilityConfig`.
  - A timing gap stays between the check and the removal of the Deployment.
  - WAO permits and reports for all other components. A block for NHC only is a new policy.
  - A block needs a new webhook for `WorkloadAvailabilityConfig`, or a deferred disable in which the spec and the operand state are different.
- WAO reports the blocked policies and the procedures in the component status (step 3.7a).
- The documents and the purge scripts give the status patch procedure (section 3.9).

Not selected now, possible follow-up work:

| Option | Reason it is not selected now |
| --- | --- |
| C. Skip only the `DELETE` rule while NHC is `Disabled` | It needs a wrapper validator and a read of the component state for each rejected delete, with a defined answer for a failed read and for an absent config. On a cluster with the Machine API, the delete can leave active remediation requests. Consider it if users report the cleanup problem. |
| B. Skip the two rules while NHC is `Disabled` | Same cost as C, and a spec change in this state gives the NHC controller wrong data after a later enable. |
| D. Remove `DELETE` from the registration | An `Enabled` NHC loses the delete protection. |
| Deferred disable with a time limit | It needs a new API field or a fixed limit, and the spec and the operand state are different during the wait. |

**D2 (2026-11-13, answers Q2, risk R1).**

- The WAO ServiceAccount gets static `list` rules for the four template kinds in the WAO bundle (option A). The RBAC marker is in `internal/webhooks/nhc/setup.go`.
- `docs/nhc-webhooks.md` gives the procedure for a template kind that is not in the bundle (section 3.9).
- An offline test compares the static rules with the labelled ClusterRoles in the bundle (section 3.8).
- The aggregated ClusterRole (option B) is not selected. Reasons:
  - The labelled roles also contain `create`, `update`, `patch` and `delete` on the remediation requests. Admission needs only `list` on templates.
  - Each labelled role, from each installer, adds its rules to the WAO ServiceAccount at run time. The CSV does not show them.
  - It adds two cluster-scoped bundle objects.
  - Unknown: if OLM v1 reverts the `rules` field of an aggregated role.
- Known limit: a duplicate template kind of a remediator from a different vendor is rejected until an administrator adds the permission. [Issue 10](https://github.com/jwmatthews/workload-availability-operator/issues/10) is the tracker.

**D3 (2026-11-13, answers Q3).**

- The readiness check of the shared listener has the name `admission`. The name `far-admission` goes away.
- Reason: four operands will have admission in the WAO manager when the work is complete (NHC, SNR, FAR and NMO have webhooks). One listener serves all of them.
- The shared package and the reconciler field from section 3.4 use names that are not specific to one component.

**D4 (2026-11-13, answers Q4, risk R3).**

- WAO copies `internal/controller/cluster/capabilities.go` without a change and adds `github.com/openshift/api` to `go.mod`.
- Reason: the list of supported control plane topologies stays equal to upstream, and a source update shows each change in a diff.

### Questions for a decision

Q1 to Q4 have the answers D1 to D4. No numbered question is open.

Items that still need an answer or a check:

- Answered on 2026-11-13: the offline tests for NHC are in a second file, `test/offline/nhc_admission_test.go`, with shared helpers.
- Answered on 2026-11-13: commit 3 and commit 5 stay separate if `make check` passes on commit 3. If not, they become one commit.
- Answered on 2026-11-13: R8, see the risk text.
- R4: an envtest check of a CRD deletion with existing `NodeHealthCheck` objects and no WAO replica.
- If OLM v1 acceptance is necessary for NHC.

This branch is on top of `feat/webhooks/far`, which is in review. A rebase can be necessary. The FAR and NHC work can also become one pull request.

## 5. Implementation sequence

Each commit must pass `make check`. Run `make test-offline` and `make test` on a clean tree before a push. Run `make bundle-reset` after each change of bundle sources.

| # | Commit | Check |
| --- | --- | --- |
| 1 | docs: add the NHC webhook plan with decisions D1 to D4 | Review |
| 2 | Share the admission client and the listener readiness between components (section 3.4). No behavior change. | `make test`, the FAR suites pass with no assertion change other than names |
| 3 | Pin the NHC module. Copy the webhook, `cluster` and `annotations` files and the upstream tests. Add `SOURCE.md` and the lint exclusion. No registration yet: keep the marker out of generation in this commit, or include commit 5 here if that is not practical. | `go build ./...`, `go test ./internal/webhooks/nhc/v1alpha1/...`, the `SOURCE.md` diff command shows only the listed adaptations |
| 4 | Test the NHC validation behavior that WAO preserves (`wao_behavior_test.go`) | `go test ./internal/webhooks/nhc/v1alpha1/...` |
| 5 | Initialize NHC admission in each WAO replica: `setup.go`, RBAC markers, scheme, `cmd/main.go`, `make manifests` | `setup_test.go`, `config/rbac/role.yaml` diff has only the planned rules, `config/webhook/manifests.yaml` has 5 entries |
| 6 | Move NHC admission registration to the WAO installation: delete patch, `make operand-manifests`, `nhc.go`, `make bundle-reset`, changed controller, component, render and offline tests | `make check`, `make test-offline`, `make test`. CSV has 5 definitions. `objects.yaml` for NHC has no registration. |
| 6a | Report the policies that admission blocks while NHC is `Disabled` (step 3.7a): definition field, reconciler message, controller tests | `make test`. The message names the object and the two procedures. |
| 6b | Clear the remediation status before the purge scripts delete a `NodeHealthCheck` | Review of the script change. `make check`. |
| 7 | Test the registered NHC handler through an API server (`admission_test.go`, `ENVTEST_PACKAGES`, race run), including `DELETE`, R4, and D1: a rejected `DELETE` and `UPDATE`, then a status patch, then the same requests pass | `make test`, `make test-envtest ENVTEST_K8S_VERSION=1.29` |
| 8 | Test the NHC admission wiring of the manager binary (`cmd/main_test.go`) | `go test ./cmd/...` with envtest assets |
| 9 | docs: add `docs/nhc-webhooks.md` and update the overview documents | Review |
| 10 | docs: add classic OLM acceptance results. Only after the user names a disposable cluster. Server-side dry-run only. | Results table in `docs/nhc-webhooks.md` |
| 11 | Optional: OLM v1 acceptance results on the same cluster | Results table |

## 6. Implementation record (2026-11-13)

The sequence of section 5 is complete except OLM v1 acceptance (commit 11). The branch is local.

| Plan commit | Result |
| --- | --- |
| 1 | `docs: add the plan for NHC admission webhooks in WAO` |
| 2 | `Share the admission client and the listener readiness between components` |
| 3 and 5 | One commit, `Initialize NHC admission in each WAO replica`. The marker of the copied file changes the bundle, so the copy and the registration could not be separate. |
| 4 | `Test the NHC validation behavior that WAO preserves` |
| 6 | `Move NHC admission registration to the WAO installation` |
| 6a | `Report the NodeHealthChecks that admission blocks while NHC is Disabled` |
| 6b | `Clear the remediation status before the purge scripts delete a NodeHealthCheck` |
| 7 | `Test the registered NHC handler through an API server` |
| 8 | `Test the NHC admission wiring of the manager binary` |
| 9 and 10 | `docs: add NHC admission operation notes and classic OLM acceptance results` |

Differences from the plan text:

- Section 3.3 planned `get` on `infrastructures`. The RBAC marker of the copied file `cluster/capabilities.go` gives `get`, `list` and `watch`. The file has no change, so WAO has the three verbs.
- Section 3.4: `cmd/main.go` adds the readiness check one time with `listener.AddReadyCheck`. The `Initialize` functions of FAR and NHC do not add it.
- Section 3.7a: the definition field is `AdmissionBlock` (a condition function and a message), not `BlockedByStatus`.
- `ENVTEST_PACKAGES` and the race detector run use `./internal/webhooks/...`, so a new component needs no Makefile change.
- The NHC module requires `cel-go` v0.29.0. WAO had v0.26.0 as an indirect requirement.

Answers to the unknowns:

- R4: a deletion of the `NodeHealthCheck` CRD completes when no listener serves, also with an object that shows a remediation (envtest, Kubernetes 1.29 and 1.36). A direct delete of a `NodeHealthCheck` needs a WAO replica.
- R8: see the risk text.

Issues for the concerns:

| Issue | Subject |
| --- | --- |
| [9](https://github.com/jwmatthews/workload-availability-operator/issues/9) | A `NodeHealthCheck` with a remediation status while NHC is `Disabled` (D1) |
| [10](https://github.com/jwmatthews/workload-availability-operator/issues/10) | Template kinds that are not in the WAO bundle (D2), and `Metal3RemediationTemplate` on OpenShift |
| [11](https://github.com/jwmatthews/workload-availability-operator/issues/11) | No removal of a registration that a previous build rendered (R8) |
| [12](https://github.com/jwmatthews/workload-availability-operator/issues/12) | Unused webhook listener of the NHC operand (R10) |
| [13](https://github.com/jwmatthews/workload-availability-operator/issues/13) | Startup dependency on the `Infrastructure` object (R2) |
| [14](https://github.com/jwmatthews/workload-availability-operator/issues/14) | The annotation check reads the presence, not the value |
| [15](https://github.com/jwmatthews/workload-availability-operator/issues/15) | No live cluster evidence for `UPDATE` and `DELETE` (R6) |
| [16](https://github.com/jwmatthews/workload-availability-operator/issues/16) | A `NodeHealthCheck` delete needs a WAO replica (R4) |
| [17](https://github.com/jwmatthews/workload-availability-operator/issues/17) | Dependency changes (R3) |
