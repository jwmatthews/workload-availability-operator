# NHC admission webhooks in WAO

The WAO manager serves the admission webhook of the Node Healthcheck Operator (NHC).
The webhook operates when the NHC operand is `Disabled` and when it is `Enabled`.
[SOURCE.md](../internal/webhooks/nhc/SOURCE.md) records the NHC source pin and the copied files.
[The plan](superpowers/plans/2026-11-13-nhc-webhooks-in-wao.md) records the decisions D1 to D4.
[FAR admission webhooks in WAO](far-webhooks.md) describes the parts that NHC and FAR share: the listener, the certificates and the installers.

## Admission entry

NHC has one entry. It has no mutating webhook, no defaulter and no conversion webhook.

| Item | Value |
| --- | --- |
| Entry name | `vnodehealthcheck.kb.io` |
| Type | Validating |
| Group, version, resource | `remediation.medik8s.io`, `v1alpha1`, `nodehealthchecks` (cluster-scoped) |
| Path | `/validate-remediation-medik8s-io-v1alpha1-nodehealthcheck` |
| Operations | `CREATE`, `UPDATE`, `DELETE` |
| `failurePolicy` | `Fail` |
| `sideEffects` | `None` |
| AdmissionReview versions | `v1` |

- The entry has a `DELETE` operation. The FAR entries do not. A delete of a `NodeHealthCheck` fails when no WAO replica can answer.
- The entry does not contain the status subresource. A status request gets no validation and needs no WAO replica.
- Classic OLM gives the admission configuration a generated name. Find the entry by its entry name and rules.

## Registration and ownership

The registration is part of the WAO installation. It is not an operand object.

1. The Kubebuilder marker in `internal/webhooks/nhc/v1alpha1/nodehealthcheck_webhook.go` is the source of the entry.
2. `make manifests` writes `config/webhook/manifests.yaml` from the markers of all copied files.
3. `make bundle-reset` gives that file to operator-sdk, which writes the `spec.webhookdefinitions` entries of the bundle CSV.
4. The installer creates the Service, the serving certificate and the admission configuration.

- Do not change `failurePolicy`.
- WAO does not apply, own, or delete the registration. The registration has no `WorkloadAvailabilityConfig` owner reference.
- The NHC operand renders no admission configuration. `config/operands/nhc/overlays/runtime/webhook_configurations_delete_patch.yaml` deletes it from the runtime objects.
- The NHC operand keeps its webhook Service and its certificate Secret. The unchanged NHC image starts a webhook listener and stops without certificate files. No registration uses that listener.
- WAO does not adopt or delete an admission configuration that a different installer created. A cluster must not have a standalone NHC installation together with WAO.
- An update from a WAO build that rendered the NHC registration is not supported. The reconciler does not remove an object that it no longer renders.

## Startup, readiness and lifecycle

- Each WAO replica does these steps before it serves:
  1. Make the dedicated client.
  2. Read the cluster capabilities: discover the API groups, and on OpenShift read `Infrastructure/cluster` for the control plane topology.
  3. Register the handler.
- A failure of a step stops the manager, also when NHC is `Disabled`. The read of the capabilities has a limit of 30 seconds.
- The capabilities are a snapshot from the start of the WAO Pod. Start the WAO Pods again after a change of the control plane topology.
- The readiness check `admission` shows the local webhook listener. The listener serves the entries of all components.
- The `admissionReady` status of `nodeHealthCheck` shows the local listener of the leader replica, in the `Enabled` state and in the `Disabled` state.
- NHC enable, NHC disable, and deletion of the `WorkloadAvailabilityConfig` do not remove the registration. An uninstall of WAO removes it.

## Permissions of the WAO ServiceAccount

| Resource | Verbs | Use |
| --- | --- | --- |
| `config.openshift.io` `infrastructures` | `get`, `list`, `watch` | Control plane topology at start. The copied file `cluster/capabilities.go` has the RBAC marker. WAO uses `get` only. |
| `selfnoderemediationtemplates`, `fenceagentsremediationtemplates`, `storagebasedremediationtemplates`, `machinedeletionremediationtemplates` | `list` | The duplicate template kind rule |

WAO reads these objects directly from the API server with a dedicated client. WAO does not keep them in a cache.
NHC admission reads no Secret.

## Admission behavior that WAO preserves

`internal/webhooks/nhc/v1alpha1/wao_behavior_test.go` contains tests for the rules that depend on the cluster or on the object status.

1. `minHealthy` and `maxUnhealthy`: exactly one, not negative. `maxUnhealthy` is not permitted with `MachineDeletionRemediationTemplate`.
2. The selector is mandatory and must parse.
3. Exactly one of `remediationTemplate` and `escalatingRemediations`.
4. Escalating remediations: a unique `order`, a `timeout` of 1 minute or more, and no duplicate template kind (see below).
5. The control plane topology must be a supported one. On a cluster that is not OpenShift, each topology is supported.
6. `UPDATE` during a remediation: a change of the selector, the remediation template or the escalating remediations is rejected.
7. `DELETE` during a remediation is rejected.

"During a remediation" means that `status.unhealthyNodes[].remediations` of the stored object is not empty.

### Duplicate template kind

A `NodeHealthCheck` with two escalating remediations of the same template kind is permitted only if each template of that kind in the cluster has the annotation `remediation.medik8s.io/multiple-templates-support`.

- The handler lists the templates of that kind in all namespaces for each such request. It does not list templates for other requests.
- The check reads the presence of the annotation, not its value. A template with the value `"false"` counts as support.
- If no template of the kind exists, the request is rejected.
- If the list fails, the request is rejected with the same text, and the manager log has the error.
- The FAR and SNR template webhooks set the annotation. The SBR and MDR template webhooks do not.

### Template kinds that are not in the WAO bundle (decision D2)

The WAO ServiceAccount can list only the four template kinds in the WAO bundle.
Standalone NHC gets the permission for each remediator from an aggregated ClusterRole. WAO does not use that role, because it also contains write permissions on remediation requests.
[Issue 10](https://github.com/jwmatthews/workload-availability-operator/issues/10) is the tracker.

For a template kind from a different vendor, a duplicate kind is rejected with this text, also when each template has the annotation:

```
Using multiple templates of same kind is not supported for this template: duplicate template kind: <kind>
```

One template of such a kind, or different kinds, are not affected.

A cluster administrator can give the permission. Replace the group, the resource and the namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: wao-nhc-admission-vendor-templates
rules:
  - apiGroups: ["<template API group>"]
    resources: ["<template resource, plural>"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: wao-nhc-admission-vendor-templates
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: wao-nhc-admission-vendor-templates
subjects:
  - kind: ServiceAccount
    name: workload-availability-operator-controller-manager
    namespace: <WAO namespace>
```

`test/offline` fails when the WAO bundle gets a labelled remediator ClusterRole with a template resource that the WAO role does not list.

## A `NodeHealthCheck` with a remediation status while NHC is `Disabled` (decision D1)

Rules 6 and 7 read the stored status. The NHC controller removes the status entry when the node is healthy.
While NHC is `Disabled`, no controller changes the status. Then these requests fail until a person intervenes:

| Request | Error text |
| --- | --- |
| Delete of the `NodeHealthCheck` | `deletion prohibited due to running remediation` |
| Change of `spec.selector` | `selector update prohibited due to running remediation` |
| Change of `spec.remediationTemplate` | `remediation template update prohibited due to running remediation` |
| Change of `spec.escalatingRemediations` | `escalating remediations update prohibited due to running remediation` |

Other spec changes pass, for example `pauseRequests`.
WAO does not prevent the change of NHC to `Disabled`, and WAO does not change the rules in the `Disabled` state.
[Issue 9](https://github.com/jwmatthews/workload-availability-operator/issues/9) is the tracker.

### Status report

While NHC is `Disabled`, the component status lists each `NodeHealthCheck` in `retainedResources` and has the phase `DisableIncomplete`.
The component message names each object that admission blocks:

```
[NodeHealthCheck <name>]: the status shows a remediation, so admission rejects the delete and a change of the selector, the remediation template or the escalating remediations; set the component to Enabled again, or remove status.unhealthyNodes with a patch to the status subresource
```

The report is information only. It changes no admission answer and no `NodeHealthCheck`.
The condition of the report is `nhcRemediating` in `internal/components/nhc.go`. A test in `internal/webhooks/nhc` compares it with the delete rule of the handler.

### How to release a blocked `NodeHealthCheck`

Use one of these procedures:

1. Set the NHC component to `Enabled`. The NHC controller continues the remediation and removes the status entry when the node is healthy.
2. Remove the entry with a patch to the status subresource. Admission does not validate this request:

   ```sh
   oc --kubeconfig <path> patch nodehealthcheck <name> --subresource=status --type=merge \
     -p '{"status":{"unhealthyNodes":null}}'
   ```

The status patch does not delete the remediation requests. Each remediation request has an owner reference to the `NodeHealthCheck`.
On a cluster with the Machine API, a request can also have an owner reference to the Machine of the node.
A delete of the `NodeHealthCheck` then does not delete that request. Find and delete the remediation requests of the policy if they must stop.

`hack/wao-purge.sh` and `hack/olmv1-purge-wao.sh` do the status patch before they delete the `NodeHealthCheck` objects.

## Contributor checks

| Command | What it checks |
| --- | --- |
| `make check` | Lint, import order, and operand manifest consistency. |
| `make test-offline` | The CSV webhook definitions, the NHC runtime objects without a registration, and the permissions of the WAO ServiceAccount. |
| `make test` | The reconciler suite and the admission suites on the default Kubernetes version. The tree must be clean. |
| `make test-envtest ENVTEST_K8S_VERSION=1.29` | The same envtest suites on Kubernetes 1.29, the CSV minimum, then admission with the race detector. |

The NHC admission tests are in these groups:

- `internal/webhooks/nhc/v1alpha1`: the NHC tests for the copied file, and the WAO behavior tests.
- `internal/webhooks/nhc`: startup errors and the topology with a stub API server, and `TestAdmissionThroughAPIServer`.
  That test sends requests through an envtest API server to the registered handler.
  The WAO manager of the test uses a user that has only the ClusterRole from `config/rbac/role.yaml`.
  It is the only test of a real `UPDATE` and `DELETE`, because a cluster test must not create a `NodeHealthCheck`.
- `internal/controller`: the status report for a `Disabled` NHC.
- `cmd`: one NHC request to the built manager binary.
- `test/offline/nhc_admission_test.go`: the bundle and the permissions.

### How to update the NHC source pin

1. Select an NHC revision that the Go module proxy can resolve. Do not add a `replace` directive with a local path.
2. Run `go get github.com/medik8s/node-healthcheck-operator/v5@<revision>`, then `go mod tidy`.
3. Copy the five files again. Apply the adaptations that [SOURCE.md](../internal/webhooks/nhc/SOURCE.md) lists.
4. Update `SOURCE.md`. Run its `diff` command.
5. Compare `isRemediating` in the copied webhook with `nhcRemediating` in `internal/components/nhc.go`.
6. Run `make manifests` and `make bundle-reset`. Review each change of the webhook definitions and of `config/rbac/role.yaml`.
7. Run the checks in the table.

## Operation on a cluster

### How to check admission

Use server-side dry-run. Do not create a real `NodeHealthCheck`: an NHC manager starts remediations for the nodes that the object selects.

```yaml
apiVersion: remediation.medik8s.io/v1alpha1
kind: NodeHealthCheck
metadata:
  name: wao-admission-check
spec:
  minHealthy: 51%
  selector:
    matchExpressions:
      - key: wao.test/no-node-has-this-label
        operator: Exists
  remediationTemplate:
    apiVersion: self-node-remediation.medik8s.io/v1alpha1
    kind: SelfNodeRemediationTemplate
    namespace: <WAO namespace>
    name: self-node-remediation-automatic-strategy-template
```

```sh
oc --kubeconfig <path> apply --dry-run=server -f nhc-check.yaml
```

- A valid request gives `created (server dry run)`. No object stays in the cluster.
- A request with `minHealthy` and `maxUnhealthy` gives `minHealthy and maxUnhealthy cannot be specified at the same time`.
- Server-side dry-run covers `CREATE` only. `UPDATE` and `DELETE` need an existing object.

### Known limits

- No automated upgrade, adoption, migration, or rollback. See the manual transition in [far-webhooks.md](far-webhooks.md).
- The NHC operand Pod serves a webhook listener that no registration uses. NHC has no argument that stops the listener.
- The validator writes the name of the `NodeHealthCheck` to the WAO manager log for each request.

## Acceptance results

### Classic OLM, 2026-11-13

Environment: one disposable OpenShift 5.0.0-rc.7 cluster (amd64, 3 control plane nodes and 3 worker nodes, control plane topology `HighlyAvailable`), classic OLM, `AllNamespaces` install mode.
The installation used `make dev-olm-deploy` from the commit that added the manager binary test for NHC. The NHC operand image was `quay.io/medik8s/node-healthcheck-operator:wao`, unchanged.
All requests for a `NodeHealthCheck` used server-side dry-run or a direct AdmissionReview. No `NodeHealthCheck` existed in the cluster at any time.
The test templates were inert objects in a test namespace: two `SelfNodeRemediationTemplate` objects with the annotation set by the test, two `FenceAgentsRemediationTemplate` objects (the FAR webhook in WAO set the annotation), and two `StorageBasedRemediationTemplate` objects without the annotation.

The 14 dry-run checks: 3 valid requests (one template, `maxUnhealthy`, two different template kinds), 8 rejections from rules 1 to 4 (both thresholds, no threshold, negative threshold, no template, template and escalating remediations, `maxUnhealthy` with `MachineDeletionRemediationTemplate`, duplicate order, timeout below one minute), and 3 duplicate kind requests (FAR admitted, SNR admitted, SBR rejected).

| Check | Result |
| --- | --- |
| Install with all components `Disabled` | CSV `Succeeded`. 2 ready WAO Pods on 2 nodes. No operand Deployment, DaemonSet, native config, policy, or remediation request. |
| Capabilities | Each WAO Pod wrote `supported control plane topology`, `HighlyAvailable`, one time at start. |
| Registrations | 5 entries: the 4 FAR entries and `vnodehealthcheck.kb.io` with `CREATE`, `UPDATE`, `DELETE`, resource `nodehealthchecks`, `failurePolicy: Fail`, CA bundle present, Service `workload-availability-operator-controller-manager-service` port 443. No owner reference. The namespace selector is empty. |
| Status | `nodeHealthCheck`: `Disabled`, `admissionReady: true`, no message. |
| Dry-run admission, NHC `Disabled`, no NHC Pod | 14 of 14 checks passed in 2 of 3 runs. In the first run, 1 request failed with a TLS handshake timeout between the client and the API server, before admission. No request persisted. |
| Non-leader replica | A direct AdmissionReview to the non-leader Pod gave the correct answers: valid `CREATE` allowed, invalid `CREATE` rejected, `DELETE` with a synthetic remediation status rejected, `DELETE` without it allowed. The leader did not change. |
| NHC `Enabled`, unchanged NHC image | NHC became `Available` with 2 NHC Pods and the console plugin Pod. No admission configuration with the legacy NHC name. The registration still points at the WAO Service (same UID). The NHC webhook Service and certificate Secret exist. 14 of 14 checks passed. |
| NHC `Disabled` again | The NHC Deployments and Services were removed. The registrations stayed (same UIDs). 14 of 14 checks passed. |
| Deletion and creation of the `WorkloadAvailabilityConfig` | The 5 registrations stayed (same UIDs). 14 of 14 checks passed without the object. |
| Replacement of the two WAO Pods, one at a time | 204 dry-run requests during the replacement. 204 passed. Each new Pod read the topology again. |
| Uninstall with `make dev-olm-undeploy` | The 5 registrations were removed. A seeded foreign configuration with the legacy NHC name was unchanged. The medik8s CRDs were removed. |

Observation: OpenShift has a ClusterRole `machine-api-controllers-metal3-remediation-aggregation` with the aggregation label for `metal3remediationtemplates`.
Thus `Metal3RemediationTemplate` is a template kind outside the WAO bundle on an OpenShift cluster. The limit of decision D2 applies to it.

Not done:

- `UPDATE` and `DELETE` through the API server on the cluster, the status report for a blocked `NodeHealthCheck`, and the status patch procedure. They need a real `NodeHealthCheck`. The envtest suites cover them.
- The duplicate kind rule for a template kind without list permission. The envtest suite covers it.
- The purge scripts with a `NodeHealthCheck` that shows a remediation. The read command of the new function was run on the cluster with no object.
- OLM v1, certificate renewal, an unsupported control plane topology, and a cluster that is not OpenShift.
- Upgrade, adoption, and migration. They are outside this release.
