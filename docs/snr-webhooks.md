# SNR admission webhooks in WAO

The WAO manager serves the admission webhooks of Self Node Remediation (SNR).
The webhooks operate when the SNR operand is `Disabled` and when it is `Enabled`.
[SOURCE.md](../internal/webhooks/snr/SOURCE.md) records the SNR source pin and the copied files.
[The plan](superpowers/plans/2026-11-13-snr-webhooks-in-wao.md) records the decisions S1 to S6.
[FAR admission webhooks in WAO](far-webhooks.md) describes the parts that all operands share: the listener, the certificates and the installers.

Terms in this document:

- **Agent**: a Pod of the SNR DaemonSet `self-node-remediation-ds`. The SNR manager creates the DaemonSet from the `SelfNodeRemediationConfig`.
- **Default config**: the `SelfNodeRemediationConfig` with the name `self-node-remediation-config` in the WAO namespace.

## Admission entries

SNR has four entries: one mutating and three validating. It has no conversion webhook.
The group is `self-node-remediation.medik8s.io`, the version is `v1alpha1`, and the three kinds are namespaced.

| Entry name | Type | Resource | Operations | `failurePolicy` |
| --- | --- | --- | --- | --- |
| `mselfnoderemediationtemplate.kb.io` | Mutating | `selfnoderemediationtemplates` | `CREATE`, `UPDATE` | `Fail` |
| `vselfnoderemediationtemplate.kb.io` | Validating | `selfnoderemediationtemplates` | `CREATE`, `UPDATE` | `Fail` |
| `vselfnoderemediationconfig.kb.io` | Validating | `selfnoderemediationconfigs` | `CREATE`, `UPDATE`, `DELETE` | `Fail` |
| `vselfnoderemediation.kb.io` | Validating | `selfnoderemediations` | `CREATE`, `UPDATE` | `Ignore` |

| Entry name | Path |
| --- | --- |
| `mselfnoderemediationtemplate.kb.io` | `/mutate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate` |
| `vselfnoderemediationtemplate.kb.io` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate` |
| `vselfnoderemediationconfig.kb.io` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig` |
| `vselfnoderemediation.kb.io` | `/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation` |

- For each entry, `sideEffects` is `None` and the AdmissionReview version is `v1`.
- SNR sets no `timeoutSeconds`. The API server default of 10 seconds applies.
- No entry contains a status subresource. A status request gets no validation and needs no WAO replica.
- Only the config entry has `DELETE`. A delete of a template or of a `SelfNodeRemediation` needs no WAO replica.
- Classic OLM gives each admission configuration a generated name. Find an entry by its entry name and rules.

### The entry with `Ignore` (decision S1)

`vselfnoderemediation.kb.io` has `failurePolicy: Ignore` in SNR. WAO keeps the value. It is the only entry of the WAO registration that does not have `Fail`.

- A remediation must not depend on the admission listener. NHC creates a `SelfNodeRemediation` for an unhealthy node, and the SNR controller updates it.
- When no WAO replica answers, a `SelfNodeRemediation` request passes with no validation.
- A request can wait for the timeout of 10 seconds before `Ignore` applies ([issue 24](https://github.com/jwmatthews/workload-availability-operator/issues/24)).
- Do not change the `failurePolicy` of an entry. `TestSNRCSVKeepsTheFailurePolicyOfEachSNREntry` in `test/offline` compares each CSV value with the copied SNR configuration.

## Registration and ownership

The registration is part of the WAO installation. It is not an operand object.

1. The Kubebuilder markers in the three files of `internal/webhooks/snr/v1alpha1/` are the source of the entries.
2. `make manifests` writes `config/webhook/manifests.yaml` from the markers of all copied files.
3. `make bundle-reset` gives that file to operator-sdk, which writes the `spec.webhookdefinitions` entries of the bundle CSV.
4. The installer creates the Service, the serving certificate and the admission configurations.

- WAO does not apply, own, or delete the registration. The registration has no `WorkloadAvailabilityConfig` owner reference.
- The SNR operand renders no admission configuration. `config/operands/snr/overlays/runtime/webhook_configurations_delete_patch.yaml` deletes the two configurations from the runtime objects.
- The SNR operand keeps its webhook Service and its certificate Secret (decision S5). The unchanged SNR image starts a webhook listener and stops without certificate files. No registration uses that listener ([issue 27](https://github.com/jwmatthews/workload-availability-operator/issues/27)).
- WAO does not adopt or delete an admission configuration that a different installer created. A cluster must not have a standalone SNR installation together with WAO.
- An update from a WAO build that rendered the SNR registrations is not supported. The reconciler does not remove an object that it no longer renders ([issue 11](https://github.com/jwmatthews/workload-availability-operator/issues/11)).

## Startup, readiness and lifecycle

- Each WAO replica does these steps before it serves:
  1. Read the namespace from the environment variable `OPERATOR_NAMESPACE` (decision S2).
  2. Read the Kubernetes version of the cluster (decision S3). The copied SNR function tries for 10 seconds.
  3. Register the three webhooks.
- A failure of a step stops the manager, also when SNR is `Disabled`.
- The Kubernetes version is a snapshot from the start of the WAO Pod.
- The readiness check `admission` shows the local webhook listener. The listener serves the entries of all components.
- The `admissionReady` status of `selfNodeRemediation` shows the local listener of the leader replica, in the `Enabled` state and in the `Disabled` state.
- SNR enable, SNR disable, and deletion of the `WorkloadAvailabilityConfig` do not remove the registration. An uninstall of WAO removes it.
- SNR admission reads no object from the cluster. It adds no permission to the WAO ServiceAccount and uses no dedicated client.

### The namespace (decision S2)

The config validator of SNR must know the namespace of the SNR installation. SNR reads it from the environment variable `DEPLOYMENT_NAMESPACE`.
The WAO Deployment sets `OPERATOR_NAMESPACE` from the namespace of the Pod. WAO installs each operand in its own namespace, so the value is the same.

- The copied file `internal/webhooks/snr/utils/namespaces.go` reads `OPERATOR_NAMESPACE`. This is the one change of copied logic.
- The copied config test sets `OPERATOR_NAMESPACE` in four locations.
- If the variable is absent, the validator rejects each config `CREATE` and the delete of the default config. Thus the manager stops at start.
- Not selected: a second variable in the WAO Deployment, a namespace field in the copied validators, and an `os.Setenv` call in `cmd/main.go`.
- [Issue 28](https://github.com/jwmatthews/workload-availability-operator/issues/28) is the tracker.

## Admission behavior that WAO preserves

The validation code and the defaulter are copies of the SNR files. WAO changes no rule.
`internal/webhooks/snr/v1alpha1/wao_behavior_test.go` contains tests for the answers that depend on the namespace and on the Kubernetes version.

`SelfNodeRemediationConfig`:

1. `CREATE` and `UPDATE`: six duration fields have a minimum value. `maxTimeForNoPeersResponse` has a minimum when it is set.
2. `CREATE` and `UPDATE`: each custom toleration and each custom node selector term of the DaemonSet must be valid.
3. `CREATE` and `UPDATE`: a warning, not a rejection, when `peerRequestTimeout` is less than `apiServerTimeout` plus 2 seconds.
4. `CREATE` (singleton rule): the name must be `self-node-remediation-config`, and the namespace must be the WAO namespace.
5. `DELETE`: a warning for the default config. No rule rejects a delete.

`SelfNodeRemediationTemplate`:

6. Mutating, `CREATE` and `UPDATE`: add the annotation `remediation.medik8s.io/multiple-templates-support: "true"` when the annotation key is absent. The defaulter does not replace a value.
7. Validating, `CREATE` and `UPDATE`: reject the strategy `OutOfServiceTaint` on Kubernetes older than 1.26.

`SelfNodeRemediation`:

8. Validating, `CREATE` and `UPDATE`: rule 7 for `spec.remediationStrategy`.

The WAO CSV has `minKubeVersion: 1.29.0`. On each supported cluster, rule 7 and rule 8 reject nothing.

### Differences from standalone SNR

| Subject | Standalone SNR | WAO |
| --- | --- | --- |
| Source of the namespace | `DEPLOYMENT_NAMESPACE` | `OPERATOR_NAMESPACE` (decision S2) |
| Failed read of the Kubernetes version at start | Continue, and reject `OutOfServiceTaint` | Stop the manager (decision S3, [issue 29](https://github.com/jwmatthews/workload-availability-operator/issues/29)) |
| Admission while the SNR manager does not run | A config or template request fails (`Fail`). A `SelfNodeRemediation` request passes with no validation (`Ignore`). | Each request is validated |
| Server of the default config and template requests of the SNR manager | The SNR manager Pod | A WAO replica ([issue 25](https://github.com/jwmatthews/workload-availability-operator/issues/25)) |
| TLS settings of the listener | The TLS profile of the OpenShift API server | The Go default settings ([issue 18](https://github.com/jwmatthews/workload-availability-operator/issues/18)) |

### The SNR manager and WAO admission

- At start, the SNR manager creates the default config and the template `self-node-remediation-automatic-strategy-template`.
- These requests go to a WAO replica through entries with `Fail`. If no replica answers, the SNR manager process stops and starts again.
- The singleton rule accepts the default config, because the SNR manager and the WAO manager are in one namespace.
- An SNR template in a WAO cluster always has the annotation key of rule 6. A removal of the annotation does not persist, because the defaulter also runs for an `UPDATE`.
  NHC admission reads the presence of that key ([issue 14](https://github.com/jwmatthews/workload-availability-operator/issues/14)).

### A `SelfNodeRemediationConfig` while SNR is `Disabled`

- Admission validates each config request in the two states. The singleton rule also applies while SNR is `Disabled`.
- A valid default config that a person creates while SNR is `Disabled` starts no agent. No controller acts on it.
  The component status lists the config in `retainedResources`, and the phase is `DisableIncomplete`.
- When SNR is set to `Enabled`, the SNR manager uses that config and starts the agents on each node.
- After a disable, the default config and the agents stay. The phase is `DisableIncomplete`. This is the WAO behavior from before the admission work.
- To remove the agents, delete the default config. The answer has the warning `The default configuration is deleted, Self Node Remediation is now disabled`.
  The text is from SNR. It does not refer to the WAO component state.
  The DaemonSet has an owner reference to the config, so the garbage collector removes it. The phase then becomes `Disabled`.
- The delete needs a WAO replica, because the config entry has `DELETE` with `Fail`.

[Issue 26](https://github.com/jwmatthews/workload-availability-operator/issues/26) is the tracker. No rule rejects a delete because of controller state, so SNR has no `AdmissionBlock` report (decision S6).

## Security record

Each row is a choice that changes what a person or a ServiceAccount can do or see.

| Choice | Selected | Concern that stays | Tracker |
| --- | --- | --- | --- |
| Permissions of the WAO ServiceAccount | No change. SNR admission reads no object. | None | None |
| `failurePolicy` | Unchanged from SNR: three entries with `Fail`, one with `Ignore` (decision S1) | A config or template request fails when no WAO replica answers. A `SelfNodeRemediation` request passes with no validation in that condition. | [24](https://github.com/jwmatthews/workload-availability-operator/issues/24), [25](https://github.com/jwmatthews/workload-availability-operator/issues/25) |
| Kubernetes version read at start | Stop the manager on an error (decision S3) | An API server error at start stops WAO admission for all operands | [29](https://github.com/jwmatthews/workload-availability-operator/issues/29) |
| Source of the namespace | `OPERATOR_NAMESPACE` of the WAO Pod (decision S2) | Two copied files are different from SNR | [28](https://github.com/jwmatthews/workload-availability-operator/issues/28) |
| Information in the admission answer | Unchanged from SNR | A user with `create` on `selfnoderemediationconfigs` learns the WAO namespace from the rejection text of rule 4 | [28](https://github.com/jwmatthews/workload-availability-operator/issues/28) |
| Log content | Unchanged from SNR | Object names and field values go to the WAO manager log. No Secret value. | None |
| TLS settings of the listener | No change in this work | The WAO listener does not apply the API server TLS profile | [18](https://github.com/jwmatthews/workload-availability-operator/issues/18) |
| SNR operand listener | Stays, with its Service and certificate (decision S5) | One network listener and one certificate Secret have no use | [27](https://github.com/jwmatthews/workload-availability-operator/issues/27) |
| Validation while SNR is `Disabled` | Rules apply in the two states | A config that is accepted while `Disabled` starts the agents on each node when SNR becomes `Enabled` | [26](https://github.com/jwmatthews/workload-availability-operator/issues/26) |
| Registration owner | OLM, from the WAO CSV | A person with write access to the admission configuration can remove an entry | None |

Controls:

- `TestWAOServiceAccountHasNoNewVerbForSNRAdmission` in `test/offline` fails when the WAO ServiceAccount gets a different verb on an SNR kind.
- The step `permissions` of `TestAdmissionThroughAPIServer` asks an API server what the user of the WAO manager can do.
- `TestWAODeploymentSetsTheNamespaceForSNRAdmission` in `test/offline` checks the variable name and its source.

## Contributor checks

| Command | What it checks |
| --- | --- |
| `make check` | Lint, import order, and operand manifest consistency. |
| `make test-offline` | The CSV webhook definitions, each `failurePolicy`, the SNR runtime objects without a registration, the permissions, and the namespace variable. |
| `make test` | The reconciler suite and the admission suites on the default Kubernetes version. The tree must be clean. |
| `make test-envtest ENVTEST_K8S_VERSION=1.29` | The same envtest suites on Kubernetes 1.29, the CSV minimum, then admission with the race detector. |

The SNR admission tests are in these groups:

- `internal/webhooks/snr/v1alpha1`: the SNR tests for the copied files, and the WAO behavior tests.
- `internal/webhooks/snr`: startup errors with a stub API server, and `TestAdmissionThroughAPIServer`.
  That test sends requests through an envtest API server to the registered handlers.
  It is the only test of an `UPDATE` through an API server and of the `Ignore` entry with no listener.
  The test of the failed version read needs 10 seconds, because the copied function tries again.
- `cmd`: two config requests to the built manager binary. They check the namespace of the manager process.
- `test/offline/snr_admission_test.go`: the bundle, the permissions and the namespace variable.

The envtest suite of NHC uses the kind `MachineDeletionRemediationTemplate` for its duplicate-kind test.
An SNR template is not usable there: that suite serves only the NHC handler, and the SNR template has a mutating entry with `Fail`.

### How to update the SNR source pin

1. Select an SNR revision that the Go module proxy can resolve. Do not add a `replace` directive with a local path.
2. Run `go get github.com/medik8s/self-node-remediation/v5@<revision>`, then `go mod tidy` and `go mod vendor`.
3. Copy the seven files of `internal/webhook/v1alpha1/` and the two files of `internal/utils/` again. Apply the adaptations that [SOURCE.md](../internal/webhooks/snr/SOURCE.md) lists. Run `make fix-imports`.
4. Update `SOURCE.md`. Run its `diff` command.
5. Read each new dependency of the validators. If a handler reads an object from the cluster, add a dedicated client and the minimum verb, as NMO does, and update the security record.
6. Compare each marker and `config/operands/snr/config/webhook/kustomization.yaml` with the WAO registration. A patch in that file needs the same patch in `config/webhook/kustomization.yaml`.
7. Run `make manifests` and `make bundle-reset`. Review each change of the webhook definitions and of `config/rbac/role.yaml`.
8. Run the checks in the table.

## Operation on a cluster

### How to check admission

Use server-side dry-run. Do not create a real `SelfNodeRemediation`: the SNR agents reboot the node that the object names.

```yaml
apiVersion: self-node-remediation.medik8s.io/v1alpha1
kind: SelfNodeRemediationConfig
metadata:
  name: self-node-remediation-config
  namespace: <WAO namespace>
spec:
  isSoftwareRebootEnabled: true
```

```sh
oc --kubeconfig <path> create --dry-run=server -f snr-config-check.yaml
```

- With no default config in the cluster, the request gives `created (server dry run)`. No object stays in the cluster.
- With a default config in the cluster, the request passes admission and the API server answers `already exists`.
- A different name gives `a name other than self-node-remediation-config is not allowed`.
- A different namespace gives `SelfNodeRemediationConfig is only allowed to be created in the namespace: <WAO namespace>`.
- `apiCheckInterval: 500ms` gives `ApiCheckInterval cannot be less than 1s`.
- `oc delete --dry-run=server` for the default config gives the warning of rule 5 and deletes nothing.
- A dry-run `CREATE` of a `SelfNodeRemediationTemplate` with `-o yaml` shows the annotation of rule 6.
- A dry-run `CREATE` of a `SelfNodeRemediation` passes. The WAO manager log has a line `selfnoderemediation-resource validate create`.

### Known limits

- No automated upgrade, adoption, migration, or rollback. See the manual transition in [far-webhooks.md](far-webhooks.md).
- The SNR operand Pods serve a webhook listener that no registration uses. SNR has no argument that stops the listener.
- The validators write the name of each object to the WAO manager log for each request.
- The config validator reads each duration without a nil check. The API server applies the CRD defaults before it calls the validator.
  The answer for a direct request without the durations was not checked ([issue 30](https://github.com/jwmatthews/workload-availability-operator/issues/30)).

## Acceptance results

### Classic OLM, 2026-11-14

Environment: one disposable OpenShift 5.0.0-rc.7 cluster (amd64, Kubernetes 1.36, 3 control plane nodes and 3 worker nodes), classic OLM.
The installation used `make dev-olm-deploy` from the commit that added the manager binary test for SNR. The SNR operand image was `quay.io/medik8s/self-node-remediation-operator:wao`, unchanged.
All requests of the checks used server-side dry-run or a direct AdmissionReview. No `SelfNodeRemediation` and no check object existed in the cluster at any time.
The SNR manager created its default config, its template and the agents when SNR was `Enabled`. No policy and no remediation request was created. No fence or recovery scenario was tested.

The dry-run checks:

1. A config with a different name (rejected by rule 4).
2. The default name in the namespace `default` (rejected by rule 4, the text names the WAO namespace).
3. A config with `apiCheckInterval: 500ms` (rejected by rule 1).
4. A valid default config. Pass when no config exists. `already exists` from the API server when the default config exists.
5. A delete of the default config, only when it exists (pass with the warning of rule 5, nothing deleted).
6. A template with the strategy `OutOfServiceTaint` (pass, the answer has the annotation of rule 6).
7. A `SelfNodeRemediation` with the strategy `OutOfServiceTaint` (pass).
8. No check object stays in the cluster.

The table shows "7 of 7" when no default config exists (check 5 does not apply) and "8 of 8" when it exists.

| Check | Result |
| --- | --- |
| Install with all components `Disabled` | CSV `Succeeded`. 2 ready WAO Pods on 2 nodes. No operand Deployment, DaemonSet, native config, policy, or remediation request. |
| Registrations | 10 entries: the 4 FAR entries, the NHC entry, the NMO entry and the 4 SNR entries. `vselfnoderemediation.kb.io` has `Ignore`. The other 3 SNR entries have `Fail`. `timeoutSeconds` is 10 for the SNR entries. CA bundle present. Service `workload-availability-operator-controller-manager-service` port 443. No owner reference. |
| Status | `selfNodeRemediation`: `Disabled`, `admissionReady: true`, no message. |
| Permissions of the WAO ServiceAccount (`oc auth can-i`) | Allowed: `list` on the three SNR kinds, `get` nodes. Not allowed: `create`, `update`, `patch`, `delete` on the three SNR kinds. `update` on `selfnoderemediations/status`. `create` DaemonSets. `list`, `patch` nodes. 20 of 20 answers as expected. |
| Start of the WAO Pods | The log has `out of service taint strategy isSupported: true` for Kubernetes 1.36. |
| Dry-run admission, SNR `Disabled`, no SNR Pod | 7 of 7 checks passed. The WAO manager log has the validator line for the `SelfNodeRemediation` request. |
| All six components `Enabled` with `hack/enable_all.sh`, unchanged operand images | Each component became `Available` in 16 seconds. The SNR Deployment had 2 ready Pods. The SNR manager created the default config, the template and the DaemonSet (6 of 6 agents ready) through WAO admission. The stored template has the annotation of rule 6. No admission configuration with a legacy SNR name. The SNR entries still point at the WAO Service (same UIDs). The SNR webhook Service and certificate Secret exist. 8 of 8 checks passed. |
| NHC, NMO and FAR admission with the SNR entries present | NHC: a valid dry-run passed, both thresholds rejected. NMO: an absent node rejected. FAR: a template with an unknown agent rejected with `unsupported fence agent`. No object persisted. |
| Non-leader replica | Direct AdmissionReview requests to the non-leader Pod gave the correct answers: config `CREATE` allowed, config `CREATE` with a different name rejected, config `UPDATE` with `peerUpdateInterval: 1s` rejected, config `DELETE` allowed with the warning, template mutate `CREATE` with the annotation patch, template `UPDATE` allowed, `SelfNodeRemediation` `UPDATE` allowed. The leader did not change. |
| Replacement of the two WAO Pods, one at a time | 192 dry-run requests during the replacement: 96 template requests through a `Fail` entry and 96 `SelfNodeRemediation` requests. All passed, and each template answer had the annotation. |
| All components `Disabled` again | The SNR Deployment and Services were removed. `selfNodeRemediation` had the phase `DisableIncomplete` with the DaemonSet and the config in `retainedResources`, and `admissionReady: true`. The 6 agents continued. The SNR entries stayed (same UIDs). 8 of 8 checks passed. |
| Delete of the default config while SNR was `Disabled` (a real delete) | The answer had the warning of rule 5. The config had no finalizer. The garbage collector removed the DaemonSet and the 6 agents. The phase became `Disabled` with no retained resource. The 6 nodes stayed `Ready`. |
| Deletion of the `WorkloadAvailabilityConfig` | The SNR entries stayed (same UIDs). 7 of 7 checks passed without the object. The NHC, NMO and FAR checks passed. |
| Uninstall with `make dev-olm-undeploy` | The 10 registrations were removed. The medik8s CRDs were removed. The 6 nodes were `Ready`. |

Observation: the Secret `self-node-remediation-certificates` stays in the namespace after the uninstall. The SNR manager creates it for the agents. The Secret was in the namespace before this installation, from earlier work.

Not done ([issue 30](https://github.com/jwmatthews/workload-availability-operator/issues/30)):

- An `UPDATE` through the API server on the cluster. The non-leader check sent `UPDATE` requests directly to a WAO Pod. The envtest suite covers `UPDATE` through an API server.
- The rejection of rule 7 and rule 8. It needs Kubernetes older than 1.26. The envtest suite and the behavior tests cover it with a stub version.
- The `Ignore` behavior with no WAO replica. The envtest suite covers it.
- A remediation by the SNR agents, and the wait of [issue 24](https://github.com/jwmatthews/workload-availability-operator/issues/24). This is not part of the admission work.
- OLM v1, certificate renewal, a custom TLS profile, and a cluster that is not OpenShift.
- Upgrade, adoption, and migration. They are outside this release.
