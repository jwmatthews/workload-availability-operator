# FAR admission webhooks in WAO

The WAO manager serves the admission webhooks of Fence Agents Remediation (FAR).
The webhooks operate when the FAR operand is `Disabled` and when it is `Enabled`.
[SOURCE.md](../internal/webhooks/far/SOURCE.md) records the FAR source pin and the copied files.

## Admission entries

All entries use group `fence-agents-remediation.medik8s.io`, version `v1alpha1`, operations `CREATE` and `UPDATE`, `failurePolicy: Fail`, `sideEffects: None`, and AdmissionReview `v1`.
There is no `DELETE` entry.

| Entry name | Type | Resource | Path |
| --- | --- | --- | --- |
| `mfenceagentsremediation.kb.io` | Mutating | `fenceagentsremediations` | `/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation` |
| `vfenceagentsremediation.kb.io` | Validating | `fenceagentsremediations` | `/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation` |
| `mfenceagentsremediationtemplate.kb.io` | Mutating | `fenceagentsremediationtemplates` | `/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate` |
| `vfenceagentsremediationtemplate.kb.io` | Validating | `fenceagentsremediationtemplates` | `/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate` |

Classic OLM gives the admission configurations generated names. Find an entry by its entry name and rules, not by the configuration name.

## Registration and ownership

The registration is part of the WAO installation. It is not an operand object.

1. The Kubebuilder markers in `internal/webhooks/far/v1alpha1/` are the source of the four entries.
2. `make manifests` writes `config/webhook/manifests.yaml` from the markers.
3. `make bundle-reset` gives that file to operator-sdk, which writes four `spec.webhookdefinitions` entries into the bundle CSV.
4. Classic OLM creates the webhook Service, the serving certificate, the certificate mount and the admission configurations.

- Do not add webhook definitions to the base CSV. Bundle generation does not use them.
- Do not change `failurePolicy`. A request must fail when no WAO replica can answer.
- The definitions point at the WAO Deployment `workload-availability-operator-controller-manager`, container port 9443.
- OLM mounts the certificate at `/tmp/k8s-webhook-server/serving-certs` as `tls.crt` and `tls.key`. The manager argument `--webhook-cert-path` selects that directory. The manager loads a renewed certificate without a restart.
- WAO does not apply, own, or delete the registration. The registration has no `WorkloadAvailabilityConfig` owner reference.
- The FAR operand renders no admission configuration. `config/operands/far/overlays/runtime/` deletes the two FAR configurations from the runtime objects.
- WAO does not adopt or delete an admission configuration that a different installer created. A cluster must not have a standalone FAR installation together with WAO.

## Startup, readiness and lifecycle

- The Deployment has 2 replicas. Each replica serves admission. Only the leader reconciles.
- Each replica does these steps before it serves:
  1. Load the agent inventory.
  2. Discover the Kubernetes version for the `OutOfServiceTaint` strategy check.
  3. Make the dedicated client.
  4. Register the four handlers.
- A failure of a step stops the manager, also when FAR is `Disabled`. The manager does not continue with an assumed value.
- The readiness check `admission` uses the local webhook listener of the Pod. It does not use the FAR Deployment or the WAO Service endpoints. A Pod without a serving certificate or without a serving listener is not ready.
- The `admissionReady` status of `fenceAgentsRemediation` shows the local listener of the leader replica, in the `Enabled` state and in the `Disabled` state.
- FAR enable, FAR disable, and deletion of the `WorkloadAvailabilityConfig` do not remove the registration.
- An uninstall of WAO removes the registration, because OLM owns it through the CSV.

## Agent inventory

FAR admission accepts a fence agent only if the agent is in the selected FAR image.
WAO does not contain the fence agents. Thus each WAO Pod collects a name list from the FAR image before the manager starts.

### Format

- The file is `/var/run/wao/far-agent-inventory/agents.txt`.
- The file is UTF-8 text with one agent name on each line. Each line ends with a newline character.
- Each name must match `^fence_[^/\s]+$` and contain no more than 255 bytes.
- The file must contain between 1 and 1024 names, no duplicate names, and no empty lines.
- The file contains no image reference and no architecture.

`internal/webhooks/far/inventory.Load` reads the file one time at manager start.
The manager stops if the file is missing or invalid.
The list is a snapshot from the start of the WAO Pod. WAO does not read the file again.

### Collection

Each WAO Pod has an init container with the name `far-agent-inventory`. It uses the selected FAR manager image.
The init container replaces the image entrypoint with this command:

```sh
sh -c 'set -eu
out=/var/run/wao/far-agent-inventory
cd /usr/sbin
ls -1d -- fence_* > "$out/agents.txt.tmp"
mv "$out/agents.txt.tmp" "$out/agents.txt"'
```

- The command reads directory entries only. It does not start the FAR manager, and it does not start a fence agent.
- The command fails if no name matches. Then the manager container does not start.
- The rename makes the final file complete or absent.
- The init container writes to an `emptyDir` volume. The manager container mounts that volume read-only.
- The collection occurs for each Pod start, also when FAR is `Disabled`. No FAR Deployment is necessary.

### Image selection

`operands.yaml` selects the FAR manager image. That selection has two destinations in `config/manager/manager.yaml`:

1. The `RELATED_IMAGE_FAR_MANAGER` environment variable of the manager container.
2. The `image` field of the `far-agent-inventory` init container.

`make operand-manifests` writes the two destinations. `make verify-operand-manifests` fails if they are different.
The init container image is a field of the WAO Deployment. An environment variable does not change it.
A tool that replaces image references for a disconnected registry must replace the two destinations.
The cluster service version (CSV) lists the FAR manager image in `relatedImages` one time, because the two references are the same.

The image reference is `repository:tag`. A tag is mutable.
Thus the tag can point to different content when a later WAO Pod or FAR operand Pod starts.
Then the inventory of a WAO Pod can be different from the agents in the FAR operand Pod.
Start the WAO Pods again after a change of the image content.

### Known differences from the FAR checker

The FAR checker calls `os.Stat(filepath.Join("/usr/sbin/", agent))`.
WAO accepts an agent only if its exact name is in the list. These differences are approved.

| Agent input or fixture | FAR checker result | WAO result |
| --- | --- | --- |
| `fence_ipmilan` | true | true |
| Name that is not in the image | false | false |
| `./fence_ipmilan`, `fence_ipmilan/`, `/fence_ipmilan` | true | false |
| `fence_wao_missing/../fence_ipmilan` | true | false |
| `fence_wao/..`, `fence_wao/../../bin/sh` | true | false |
| `/usr/sbin/fence_ipmilan` | false | false |
| `fence_ipmilan/wao-child` | ENOTDIR error | false |
| Input containing NUL, or excessive length | error | false |
| Broken symlink named `fence_*` | false | true, if the image contains one |
| Agent added to or removed from the image after Pod start | current state | state at Pod start |

WAO does not check that an agent is executable or that it is a regular file.

## Secret access and the Secret logging concern

FAR parameters can come from Secrets. Admission must read those Secrets to check the parameters.
Thus the WAO manager has cluster-wide `get` permission on Secrets. It has no `list`, `watch`, or write permission on Secrets.
WAO reads each Secret directly from the API server with a dedicated client. WAO does not keep Secrets in a cache.

### Concern: an action value from a Secret can go into the manager log

The FAR function `validateFenceAction` in `api/v1alpha1/fenceagentsremediation_params.go` contains this log statement:

```go
paramsLog.Error(err, "can't build CR with this action attribute", "action", paramVal)
```

- `paramVal` is the value of the `action` or `--action` parameter. This value can come from a Secret.
- The statement operates only when the value is not empty, not `reboot`, and not `off`.
- Standalone FAR writes this value to the FAR manager log. WAO imports the same code, thus the value goes into the WAO manager log.

A fix is outside the scope of the FAR webhook work. WAO adds no logging adapter and makes no FAR logging change.
[Issue 2](https://github.com/jwmatthews/workload-availability-operator/issues/2) is the tracker for a later fix.

### Rules for tests

Use only synthetic Secret values in tests and on clusters.
Do not read, copy, or log production Secret values.

## Admission behavior that WAO preserves

WAO serves the same four webhook entries as standalone FAR, for `CREATE` and `UPDATE` only.
`internal/webhooks/far/v1alpha1/wao_behavior_test.go` contains tests for each rule in this section.

### Defaulting of the shared Secret name

The legacy Secret name is `fence-agents-credentials-shared`. The defaulter looks for that Secret in the namespace of the admitted resource.
A `CREATE` request has no creation timestamp. An `UPDATE` request has one.

| Operation | Reference before mutation | Legacy Secret | Reference after mutation |
| --- | --- | --- | --- |
| CREATE | nil | exists | legacy name |
| CREATE | nil | absent | nil |
| UPDATE | nil | exists | nil |
| CREATE or UPDATE | legacy name | absent | nil |
| CREATE or UPDATE | legacy name | exists | legacy name |
| CREATE or UPDATE | custom name | exists or absent | unchanged, no Secret read |
| CREATE or UPDATE | pointer to an empty string | exists or absent | unchanged, no Secret read |
| CREATE or UPDATE | nil or legacy name | read fails (Forbidden or transport error) | the request fails |

### Other rules

- The template defaulter adds `remediation.medik8s.io/multiple-templates-support: "true"` only when the annotation is absent. It keeps an existing value, including `"false"`.
- A template `UPDATE` that removes the legacy Secret reference fails while the legacy Secret exists. A `FenceAgentsRemediation` `UPDATE` has no such check.
- The `action` and `--action` parameters accept only an empty value, `reboot`, or `off`.
- A node parameter overrides a shared parameter. A node Secret value overrides a shared Secret value.
- A parameter name in the resource and the same name in a Secret is an error.
- The validators render `{{.NodeName}}` in shared parameters and in shared Secret values.

### Shared-only parameter condition

FAR rejects a resource that has only shared parameters, or only a shared Secret, when no value contains a `{{.NodeName}}` template.
The error is `invalid spec: mandatory parameters are missing`.
WAO preserves this condition. A change to this condition needs a separate decision in FAR.

## Contributor checks

| Command | What it checks |
| --- | --- |
| `make check` | Lint, import order, and operand manifest consistency, including the two FAR image destinations. |
| `make test-offline` | Bundle webhook definitions, the WAO Deployment (replicas, init container, mounts), the Secret permission, and image synchronization. |
| `make test` | The reconciler suite and the FAR admission suites on the default Kubernetes version. The tree must be clean. |
| `make test-envtest ENVTEST_K8S_VERSION=1.29` | The same envtest suites on Kubernetes 1.29, the CSV minimum, then FAR admission with the race detector. |

CI runs the envtest suites on Kubernetes 1.29 and on the default version.

The FAR admission tests are in three groups:

- `internal/webhooks/far/inventory`: the inventory parser and the name lookup.
- `internal/webhooks/far/v1alpha1`: the FAR tests for the copied files, and the WAO behavior tests.
- `cmd`: tests that start the built manager binary with an envtest API server. They check the flags, the stop without an inventory, readiness, and one admission request.
- `internal/webhooks/far`: startup errors, readiness, and `TestAdmissionThroughAPIServer`.
  That test sends requests through an envtest API server to the four registered handlers.
  It also checks a second replica without the leader lease, and the `Fail` policy when no listener serves.

### How to update a dependency

1. Update the Go toolchain, controller-runtime, and the Kubernetes modules in a change that has no FAR change.
2. Keep `go.mod`, the `Dockerfile`, and the two CI workflows on the same Go toolchain. `test/offline` checks this.
3. Run the checks in the table on Kubernetes 1.29 and on the default version.

### How to update the FAR source pin

1. Select a FAR revision that the Go module proxy can resolve. Do not add a `replace` directive with a local path.
2. Run `go get github.com/medik8s/fence-agents-remediation/v5@<revision>`, then `go mod tidy`.
3. Copy the two webhook files and their tests again. Apply the adaptations that [SOURCE.md](../internal/webhooks/far/SOURCE.md) lists.
4. Update `SOURCE.md`.
5. Run `make manifests` and `make bundle-reset`. Review each change of the webhook definitions.
6. Run the checks in the table.

## Operation on a cluster

### How to check admission

Use server-side dry-run with synthetic values. Do not create a real `FenceAgentsRemediation`: a FAR manager fences the node that the object names.

```yaml
apiVersion: fence-agents-remediation.medik8s.io/v1alpha1
kind: FenceAgentsRemediation
metadata:
  name: wao-admission-check
spec:
  agent: fence_ipmilan
  remediationStrategy: ResourceDeletion
  nodeparameters:
    --ipport:
      worker-test: "6230"
```

```sh
oc --kubeconfig <path> -n <test namespace> apply --dry-run=server -f far-check.yaml
```

- The field names `nodeparameters` and `sharedparameters` have no capital letters. The API server removes an unknown field such as `nodeParameters`, and then FAR reports `mandatory parameters are missing`.
- A valid request gives `created (server dry run)`. No object stays in the cluster.
- A request with an agent that is not in the inventory gives `unsupported fence agent: <name>`.

### How to start the WAO Pods again

Delete the WAO Pods one at a time. Wait for the new Pod to become ready before you delete the subsequent Pod.
OLM owns the Deployment template, and it removes the annotation that `oc rollout restart` adds.

### Certificate providers

WAO has one certificate interface: the files `tls.crt` and `tls.key` in `/tmp/k8s-webhook-server/serving-certs`.
The installer supplies the files and the CA bundle of the registrations. WAO and the FAR validation code contain no provider logic.

| Installer | Certificate provider | Registration names |
| --- | --- | --- |
| Classic OLM (required for this release) | OLM makes a CA and a serving certificate. | `<entry name>-<suffix>` |
| OLM v1 on OpenShift 5.0 | OpenShift service-ca, through annotations on the Service and on the registrations. | `<entry name>` |

OLM v1 selects its provider with feature gates of operator-controller.
On the tested cluster, `WebhookProviderOpenshiftServiceCA` was true and `WebhookProviderCertManager` was false.
Those gates are installer settings. They are not WAO or FAR arguments.
Other OLM v1 distributions, versions, and the cert-manager provider have no test evidence. Do not claim support for them.

### Certificate renewal

The manager loads renewed certificate files without a restart.

- Classic OLM renews the serving certificate before it expires. It has no supported command that starts a renewal. Do not delete its certificate Secret to simulate a renewal.
- OpenShift service-ca makes a new certificate when you delete the certificate Secret of the Service. That is the OpenShift procedure for a manual rotation.

### OLM v1 limits

- An OLM v1 installation fails while cluster-scoped objects of a previous classic OLM installation are in the cluster. `make dev-olm-undeploy` leaves the bundle ClusterRoles and one ClusterRoleBinding. Delete them before an OLM v1 installation.
- Do not install the package with classic OLM and with OLM v1 at the same time.

### Manual transition from the six separate operators

This release has no automated upgrade, adoption, migration, or rollback. Do the transition in a maintenance window:

1. Make a backup of the medik8s custom resources.
2. Uninstall all medik8s operators. Make sure that no FAR admission configuration of the old installation stays.
3. Install WAO. All components are `Disabled`.
4. Enable the necessary components in `workloadavailabilityconfig/cluster`.
5. Create the custom resources again from the backup.

FAR custom resources that you create in step 5 go through WAO admission. The inventory of the selected FAR image applies.

## Acceptance results

### Classic OLM, 2026-11-12

Environment: one disposable OpenShift 5.0.0-rc.7 cluster (Kubernetes 1.36.5, amd64, 3 control plane nodes and 3 worker nodes), classic OLM, `AllNamespaces` install mode.
The installation used `make dev-olm-deploy` from the commit that added the envtest suite. The FAR operand image was `quay.io/medik8s/fence-agents-remediation-operator:wao`, without the `--enable-webhooks` flag.
Certificate provider: classic OLM (the Secret `workload-availability-operator-controller-manager-service-cert`, owner `ClusterServiceVersion`).
All Secret values were synthetic. All FAR requests used server-side dry-run.

| Check | Result |
| --- | --- |
| Install with all components `Disabled` | CSV `Succeeded`. No operand Deployment, DaemonSet, native config, policy, or remediation request. |
| WAO replicas | 2 ready Pods on 2 different nodes. |
| Inventory | The init container completed in each Pod. Each Pod has 31 names. The init container command reads directory entries only. |
| Registrations | 4 entries with the table values, `failurePolicy: Fail`, CA bundle present, Service `workload-availability-operator-controller-manager-service` port 443 to container port 9443. No owner reference. The namespace selector is empty. |
| Certificate mount | OLM mounts `tls.crt` and `tls.key` at `/tmp/k8s-webhook-server/serving-certs`. |
| Status | `fenceAgentsRemediation`: `Disabled`, `admissionReady: true`, no message. |
| Dry-run admission, FAR `Disabled`, no FAR Pod | 14 of 14 checks passed: valid FAR and template, unsupported agent, agent with a path, invalid action, annotation added, annotation `"false"` kept, synthetic Secret valid, invalid, and absent, legacy Secret name defaulting. No request persisted. |
| Non-leader replica | A direct AdmissionReview to the non-leader Pod gave the correct allow and deny answers. The leader did not change. |
| Replacement of the two WAO Pods, one at a time | 349 dry-run requests during the replacement. 348 passed. 1 failed with a TLS handshake timeout between the client and the API server, before admission. The inventory was collected again in each new Pod. The leader changed. |
| `oc rollout restart` | OLM reverted the template change. Use Pod deletion. |
| Deletion and creation of the `WorkloadAvailabilityConfig` | The 4 registrations stayed (same UIDs). Dry-run admission continued to operate without the object. |
| FAR `Enabled`, unchanged FAR image | FAR became `Available` with 2 FAR Pods and a service-ca certificate. No legacy FAR admission configuration. The registrations still point at the WAO Service. 14 of 14 checks passed. |
| FAR `Disabled` again | The FAR Deployment and Services were removed. The registrations stayed. 14 of 14 checks passed. |
| Secret logging concern | A synthetic invalid action value from a synthetic Secret was in the log of the WAO Pod that answered the request. This agrees with the concern above. |
| Uninstall with `make dev-olm-undeploy` | The 4 registrations were removed. A seeded foreign configuration with the legacy FAR name was unchanged. `operator-sdk cleanup --delete-all` also removed the medik8s CRDs. |

Not done:

- Certificate renewal on the cluster. Classic OLM has no supported control to start a renewal.
- Upgrade, adoption, and migration. They are outside this release.

### OLM v1, 2026-11-12

Environment: the same cluster, OLM v1 of OpenShift 5.0.0-rc.7 (`olm.operatorframework.io/v1`), `ClusterCatalog` and `ClusterExtension` from `config/catalog/olmv1/`, with the same `registry+v1` bundle.
Certificate provider: OpenShift service-ca (`WebhookProviderOpenshiftServiceCA=true`, `WebhookProviderCertManager=false`).
The FAR operand image was the published `:wao` image, without the `--enable-webhooks` flag.

| Check | Result |
| --- | --- |
| Bundle installation | `Installed=True` after the removal of 21 ClusterRoles and 1 ClusterRoleBinding that the classic OLM uninstall left. Before that removal, the installation failed with `already exists ... cannot be managed by operator-controller`. |
| Generated objects | The Service has service-ca serving certificate annotations. The 4 registrations have the entry names as object names, the `inject-cabundle` annotation, a CA bundle, and `failurePolicy: Fail`. |
| Certificate mount | `tls.crt` and `tls.key` at `/tmp/k8s-webhook-server/serving-certs`, the same interface as classic OLM. |
| All components `Disabled` | 2 ready WAO Pods on 2 nodes, 31 inventory names in each Pod, `admissionReady: true`. 14 of 14 dry-run checks passed. |
| Certificate renewal | The certificate Secret was deleted. service-ca made a new Secret in 1 second. The two replicas served a new certificate in less than 1 minute, with no Pod restart. 589 of 589 dry-run requests passed during the renewal. |
| FAR `Enabled`, then `Disabled`, then deletion of the `WorkloadAvailabilityConfig` | The registrations stayed (same UIDs). 14 of 14 dry-run checks passed in each state. |
| Uninstall (deletion of the `ClusterExtension`) | The 4 registrations, the CRDs, and the cluster-scoped RBAC objects were removed. |

Not done under OLM v1: the cert-manager provider, other OLM v1 versions, and FAR with `--enable-webhooks=false`.
