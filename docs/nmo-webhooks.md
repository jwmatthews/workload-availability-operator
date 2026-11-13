# NMO admission webhook in WAO

The WAO manager serves the admission webhook of the Node Maintenance Operator (NMO).
The webhook operates when the NMO operand is `Disabled` and when it is `Enabled`.
[SOURCE.md](../internal/webhooks/nmo/SOURCE.md) records the NMO source pin and the copied files.
[The plan](superpowers/plans/2026-11-13-nmo-webhooks-in-wao.md) records the decisions N1 to N6.
[FAR admission webhooks in WAO](far-webhooks.md) describes the parts that all operands share: the listener, the certificates and the installers.

## Admission entry

NMO has one entry. It has no mutating webhook, no defaulter and no conversion webhook.

| Item | Value |
| --- | --- |
| Entry name | `vnodemaintenance.kb.io` |
| Type | Validating |
| Group, version, resource | `nodemaintenance.medik8s.io`, `v1beta1`, `nodemaintenances` (cluster-scoped) |
| Path | `/validate-nodemaintenance-medik8s-io-v1beta1-nodemaintenance` |
| Operations | `CREATE`, `UPDATE` |
| `failurePolicy` | `Fail` |
| `sideEffects` | `None` |
| AdmissionReview versions | `v1` |

- The entry has no `DELETE` operation. A delete of a `NodeMaintenance` needs no WAO replica.
- The entry does not contain the status subresource. A status request gets no validation and needs no WAO replica.
- A `CREATE` or an `UPDATE` fails when no WAO replica can answer.
- Classic OLM gives the admission configuration a generated name. Find the entry by its entry name and rules.

## Registration and ownership

The registration is part of the WAO installation. It is not an operand object.

1. The Kubebuilder marker in `internal/webhooks/nmo/v1beta1/nodemaintenance_webhook.go` is the source of the entry.
2. `make manifests` writes `config/webhook/manifests.yaml` from the markers of all copied files.
3. `make bundle-reset` gives that file to operator-sdk, which writes the `spec.webhookdefinitions` entries of the bundle CSV.
4. The installer creates the Service, the serving certificate and the admission configuration.

- Do not change `failurePolicy`.
- WAO does not apply, own, or delete the registration. The registration has no `WorkloadAvailabilityConfig` owner reference.
- The NMO operand renders no admission configuration. `config/operands/nmo/overlays/runtime/webhook_configurations_delete_patch.yaml` deletes it from the runtime objects.
- The NMO operand keeps its webhook Service and its certificate Secret. The unchanged NMO image starts a webhook listener and stops without certificate files. No registration uses that listener ([issue 20](https://github.com/jwmatthews/workload-availability-operator/issues/20)).
- WAO does not adopt or delete an admission configuration that a different installer created. A cluster must not have a standalone NMO installation together with WAO.
- An update from a WAO build that rendered the NMO registration is not supported. The reconciler does not remove an object that it no longer renders ([issue 11](https://github.com/jwmatthews/workload-availability-operator/issues/11)).

## Startup, readiness and lifecycle

- Each WAO replica does these steps before it serves:
  1. Make the dedicated client.
  2. Check if the cluster is OpenShift. The check is the function of the NMO manager: it looks for the group version `config.openshift.io/v1` in the API discovery.
  3. Register the handler.
- A failure of a step stops the manager, also when NMO is `Disabled`.
- The OpenShift answer is a snapshot from the start of the WAO Pod.
- The readiness check `admission` shows the local webhook listener. The listener serves the entries of all components.
- The `admissionReady` status of `nodeMaintenance` shows the local listener of the leader replica, in the `Enabled` state and in the `Disabled` state.
- NMO enable, NMO disable, and deletion of the `WorkloadAvailabilityConfig` do not remove the registration. An uninstall of WAO removes it.

## Admission behavior that WAO preserves

The validation code is a copy of the NMO file. WAO changes no rule.
`internal/webhooks/nmo/v1beta1/wao_behavior_test.go` contains tests for the answers that depend on the cluster.

1. `CREATE`: a Node with the name `spec.nodeName` must exist.
2. `CREATE`: no other `NodeMaintenance` has the same `spec.nodeName`.
3. `CREATE`, on OpenShift, for a control plane node (the etcd quorum rule):
   - `openshift-etcd` must have exactly one PodDisruptionBudget. If not, the request is rejected.
   - If `status.disruptionsAllowed` of that object is 1 or more, the request passes.
   - If not, the request passes only when the guard Pod of the node is absent or is not ready.
4. `UPDATE`: `spec.nodeName` cannot change.

If a read fails, the handler rejects the request. It does not pass a request that it cannot check.

### Differences from standalone NMO

| Subject | Standalone NMO | WAO |
| --- | --- | --- |
| Source of the handler data | The manager cache | A direct read from the API server |
| Rule 2 and a `NodeMaintenance` that was created a moment before | The cache can be late, so two objects for one node can pass | The handler sees the stored object |
| Admission while the NMO manager does not run | The request fails (`failurePolicy: Fail`) | The request is validated |
| TLS settings of the listener | The TLS profile of the OpenShift API server | The Go default settings ([issue 18](https://github.com/jwmatthews/workload-availability-operator/issues/18)) |

### A `NodeMaintenance` while NMO is `Disabled`

- Admission validates and accepts the object. No controller acts on it. The object gets no status and no finalizer.
- The component status lists the object in `retainedResources`.
- When NMO is set to `Enabled`, the NMO controller cordons and drains the node. The etcd quorum rule was checked at the time of the `CREATE`, not at the time of the enable.
- Rule 2 rejects a second object for the same node while the first object stays. Delete the first object to release the node name.
- An object that got the NMO finalizer in the `Enabled` state cannot complete a delete while NMO is `Disabled`. Admission does not cause this.

[Issue 22](https://github.com/jwmatthews/workload-availability-operator/issues/22) is the tracker. No rule rejects a delete, so NMO has no `AdmissionBlock` report (decision N3).

## Security record

Each row is a choice that changes what a person or a ServiceAccount can do or see.

| Choice | Selected | Concern that stays | Tracker |
| --- | --- | --- | --- |
| Permissions of the WAO ServiceAccount | Cluster scope, minimum verbs, no write verb (decision N1) | WAO can list the Pods of each namespace | [19](https://github.com/jwmatthews/workload-availability-operator/issues/19) |
| `failurePolicy` | `Fail`, unchanged from NMO | A `NodeMaintenance` `CREATE` or `UPDATE` fails when no WAO replica answers. Two replicas decrease the probability. | `AGENTS.md` |
| Answer when a read fails | Reject, unchanged from NMO | A missing permission or an API server error rejects the request | None |
| Source of the handler data | Direct read from the API server | Each `CREATE` adds 2 to 5 read requests, also with dry-run | [21](https://github.com/jwmatthews/workload-availability-operator/issues/21) |
| Information in the admission answer | Unchanged from NMO | A user with `create` on `nodemaintenances` learns if a node name exists and if etcd permits a disruption. With WAO, the answer is available also while NMO is `Disabled`. | [21](https://github.com/jwmatthews/workload-availability-operator/issues/21) |
| Log content | Unchanged from NMO | Object names and node names go to the WAO manager log. No Secret value. | None |
| TLS settings of the listener | No change in this work | The WAO listener does not apply the API server TLS profile | [18](https://github.com/jwmatthews/workload-availability-operator/issues/18) |
| NMO operand listener | Stays, with its Service and certificate (decision N4) | One network listener and one certificate Secret have no use | [20](https://github.com/jwmatthews/workload-availability-operator/issues/20) |
| OpenShift check at start | Stop the manager on a discovery error (decision N2) | A discovery error stops WAO admission for all operands. An assumed answer could disable the etcd quorum rule. | [13](https://github.com/jwmatthews/workload-availability-operator/issues/13) |
| Registration owner | OLM, from the WAO CSV | A person with write access to the admission configuration can remove the entry | None |

### Permissions of the WAO ServiceAccount (decision N1)

| Resource | Verbs | Use | Verbs of the standalone NMO ClusterRole |
| --- | --- | --- | --- |
| `nodes` | `get` | Rule 1 and rule 3 | `get`, `list`, `watch`, `patch`, `update` |
| `pods` | `list` | Rule 3, in `openshift-etcd` only | `get`, `list`, `watch`, and `create` on `pods/eviction` |
| `policy` `poddisruptionbudgets` | `list` | Rule 3, in `openshift-etcd` only | `get`, `list`, `watch` |
| `nodemaintenances` | `get`, `list`, `watch` | Rule 2. The WAO reconciler had this rule before the admission work. | All verbs |

- The scope is the cluster, as in the NMO ClusterRole. NMO has no namespaced Role.
- The verbs are only those that the handler calls. The handler has no cache, so `watch` is not necessary.
- WAO has no write verb on nodes and no eviction permission. Those verbs permit a cordon and a drain of a node.
- Concern: the `list` permission on Pods has the scope of the cluster. The handler reads only `openshift-etcd`. A Pod object can contain environment values and the names of Secrets. It does not contain Secret data.
- Not selected: a Role and a RoleBinding in `openshift-etcd`. It is the minimum permission, but NMO does not have this structure. It was not checked if OLM installs a bundle Role into a namespace that is not the install namespace.
- Controls: `TestWAOServiceAccountHasMinimumVerbsForNMOAdmission` in `test/offline` fails when the bundle gives a different verb. The step `permissions` of `TestAdmissionThroughAPIServer` asks an API server what the user of the WAO manager can do.
- A missing permission rejects each `NodeMaintenance` for a control plane node. The user sees the error text of the failed list.

## Contributor checks

| Command | What it checks |
| --- | --- |
| `make check` | Lint, import order, and operand manifest consistency. |
| `make test-offline` | The CSV webhook definition, the NMO runtime objects without a registration, and the permissions of the WAO ServiceAccount. |
| `make test` | The reconciler suite and the admission suites on the default Kubernetes version. The tree must be clean. |
| `make test-envtest ENVTEST_K8S_VERSION=1.29` | The same envtest suites on Kubernetes 1.29, the CSV minimum, then admission with the race detector. |

The NMO admission tests are in these groups:

- `internal/webhooks/nmo/v1beta1`: the NMO tests for the copied file, and the WAO behavior tests.
- `internal/webhooks/nmo`: startup errors with a stub API server, and `TestAdmissionThroughAPIServer`.
  That test sends requests through an envtest API server to the registered handler.
  The WAO manager of the test uses a user that has only the ClusterRole from `config/rbac/role.yaml`.
  It is the only test of rule 2, rule 4 and the rejection of rule 3 with a real request, because a cluster test must not create a `NodeMaintenance`.
- `cmd`: one NMO request to the built manager binary.
- `test/offline/nmo_admission_test.go`: the bundle and the permissions.

### How to update the NMO source pin

1. Select an NMO revision that the Go module proxy can resolve. Do not add a `replace` directive with a local path.
2. Run `go get github.com/medik8s/node-maintenance-operator/v5@<revision>`, then `go mod tidy` and `go mod vendor`.
3. Copy the three files again. Apply the adaptations that [SOURCE.md](../internal/webhooks/nmo/SOURCE.md) lists.
4. Update `SOURCE.md`. Run its `diff` command.
5. Read each client call of the new validator. If the handler reads a new resource, add the minimum verb to the RBAC markers in `internal/webhooks/nmo/setup.go` and update the security record.
6. Run `make manifests` and `make bundle-reset`. Review each change of the webhook definitions and of `config/rbac/role.yaml`.
7. Run the checks in the table.

## Operation on a cluster

### How to check admission

Use server-side dry-run. Do not create a real `NodeMaintenance`: an NMO manager cordons and drains the node.

```yaml
apiVersion: nodemaintenance.medik8s.io/v1beta1
kind: NodeMaintenance
metadata:
  name: wao-admission-check
spec:
  nodeName: <node name>
  reason: wao-admission-check
```

```sh
oc --kubeconfig <path> create --dry-run=server -f nmo-check.yaml
```

- A request for a worker node gives `created (server dry run)`. No object stays in the cluster.
- A request with a node name that does not exist gives `invalid nodeName, no node with name <name> found`.
- A request for a control plane node passes when the PodDisruptionBudget in `openshift-etcd` shows 1 or more allowed disruptions.
- Server-side dry-run covers `CREATE` only. Rule 2 and rule 4 need an existing object.

### Known limits

- No automated upgrade, adoption, migration, or rollback. See the manual transition in [far-webhooks.md](far-webhooks.md).
- The NMO operand Pod serves a webhook listener that no registration uses. NMO has no argument that stops the listener.
- The validator writes the name of the `NodeMaintenance` and the node name to the WAO manager log for each request.

## Acceptance results

### Classic OLM, 2026-11-13

Environment: one disposable OpenShift 5.0.0-rc.7 cluster (amd64, 3 control plane nodes and 3 worker nodes), classic OLM.
The installation used `make dev-olm-deploy` from the commit that added the manager binary test for NMO. The NMO operand image was `quay.io/medik8s/node-maintenance-operator:wao`, unchanged.
All requests for a `NodeMaintenance` used server-side dry-run or a direct AdmissionReview. No `NodeMaintenance` existed in the cluster at any time.
No policy, native config or remediation request was created. No fence or recovery scenario was tested.

The 4 dry-run checks: a node name that does not exist (rejected by rule 1), a worker node (pass), a control plane node while the etcd PodDisruptionBudget showed 1 allowed disruption (pass by rule 3), and an object without `spec.nodeName` (rejected by the CRD schema).

| Check | Result |
| --- | --- |
| Install with all components `Disabled` | CSV `Succeeded`. 2 ready WAO Pods on 2 nodes. No operand Deployment, DaemonSet, native config, policy, or remediation request. |
| Registrations | 6 entries: the 4 FAR entries, the NHC entry, and `vnodemaintenance.kb.io` with `CREATE`, `UPDATE`, resource `nodemaintenances`, `failurePolicy: Fail`, CA bundle present, Service `workload-availability-operator-controller-manager-service` port 443. No owner reference. |
| Status | `nodeMaintenance`: `Disabled`, `admissionReady: true`, no message. |
| Permissions of the WAO ServiceAccount (`oc auth can-i`) | Allowed: `get` nodes, `list` pods and `list` poddisruptionbudgets in `openshift-etcd`, `list` nodemaintenances. Not allowed: `list`, `patch`, `update` nodes. `get`, `watch`, `delete` pods. `create` pods/eviction. `get` poddisruptionbudgets. `list` secrets in `openshift-etcd`. `create`, `delete` nodemaintenances. 15 of 15 answers as expected. |
| Dry-run admission, NMO `Disabled`, no NMO Pod | 4 of 4 checks passed. The WAO manager log has the validator lines for each request. |
| Non-leader replica | A direct AdmissionReview to the non-leader Pod gave the correct answers: `CREATE` for a worker node allowed, `CREATE` for an absent node rejected, `UPDATE` with a changed node name rejected, `UPDATE` with the same node name allowed. The leader did not change. |
| All six components `Enabled` with `hack/enable_all.sh`, unchanged operand images | Each component became `Available`. The NMO Deployment had 1 ready Pod. No admission configuration with the legacy NMO name. The NMO entry still points at the WAO Service (same UID). The NMO webhook Service and certificate Secret exist. 4 of 4 checks passed. |
| NHC and FAR admission with the NMO entry present | NHC: a valid dry-run passed, both thresholds rejected. FAR: a template with an unknown agent rejected with `unsupported fence agent`. No object persisted. |
| All components `Disabled` again | The NMO Deployment and Services were removed. The NMO entry stayed (same UID). 4 of 4 checks passed. `selfNodeRemediation` had the phase `DisableIncomplete`, because SNR keeps its DaemonSet and its config. This is the documented WAO behavior and not an effect of this work. |
| Deletion of the `WorkloadAvailabilityConfig` | The NMO entry stayed (same UID). 4 of 4 checks passed without the object. |
| Replacement of the two WAO Pods, one at a time | 608 dry-run requests during the replacement. 607 passed. 1 request failed with a TLS handshake timeout between the client and the API server, before admission. The new WAO Pods made the default config again. |
| Uninstall with `make dev-olm-undeploy` | The 6 registrations were removed. A seeded foreign configuration with the legacy NMO name was unchanged. The medik8s CRDs were removed. The 6 nodes were `Ready`. |

Not done ([issue 23](https://github.com/jwmatthews/workload-availability-operator/issues/23)):

- Rule 2 and rule 4 through the API server on the cluster. They need a real `NodeMaintenance`. The envtest suite covers them.
- The rejection of the etcd quorum rule. It needs an etcd member that is not healthy. The envtest suite and the behavior tests cover it.
- A drain by the NMO operand. This is not part of the admission work.
- OLM v1, certificate renewal, a custom TLS profile, and a cluster that is not OpenShift.
- Upgrade, adoption, and migration. They are outside this release.
