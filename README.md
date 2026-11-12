# WAO — Workload Availability Operator

The Workload Availability Operator (WAO) is a single operator that consolidates the [medik8s](https://github.com/medik8s) operators and deploys them as managed operands.

Start with the [overview and first trial](docs/Overview.md) for the design,
key decisions, and a guided installation.

WAO installs all six upstream components as managed operands:

| Component | What it does |
| --- | --- |
| [Node Health Check (NHC)](https://github.com/medik8s/node-healthcheck-operator) | Watches node health and creates remediation requests from templates |
| [Self Node Remediation (SNR)](https://github.com/medik8s/self-node-remediation) | Remediates unhealthy nodes with per-node agents that reboot themselves |
| [Storage Based Remediation (SBR)](https://github.com/medik8s/storage-based-remediation) | Remediates nodes through shared-storage messaging and per-node agents |
| [Fence Agents Remediation (FAR)](https://github.com/medik8s/fence-agents-remediation) | Remediates nodes by running fence agents against their management interfaces |
| [Machine Deletion Remediation (MDR)](https://github.com/medik8s/machine-deletion-remediation) | Remediates nodes by deleting their Machine so the Machine API replaces it |
| [Node Maintenance (NMO)](https://github.com/medik8s/node-maintenance-operator) | Cordons and drains a node on explicit request |

## The main idea

Today each component is a separate OLM package with its own CSV, its own API
ownership and its own upgrade lifecycle. WAO replaces that with **one OLM package
that owns all the component APIs** plus a cluster-scoped `WorkloadAvailabilityConfig`
named `cluster`. A lifecycle controller installs each component's unchanged
controller-manager as a separate operand when an administrator enables it. Every
component starts with **`enabled: false`**.

```sh
❯ oc get workloadavailabilityconfig -o yaml
apiVersion: v1
items:
- apiVersion: workloadavailability.medik8s.io/v1alpha1
  kind: WorkloadAvailabilityConfig
  metadata:
    name: cluster
  ...
  spec:
    components:
      fenceAgentsRemediation:
        enabled: false
      machineDeletionRemediation:
        enabled: false
      nodeHealthCheck:
        enabled: false
      nodeMaintenance:
        enabled: false
      selfNodeRemediation:
        enabled: false
      storageBasedRemediation:
        enabled: false
    ...
```
This operator does NOT intend to change the behavior or functionality of the underlying components.  Success for this operator is to simplify the publishing of the operands as a single entity.  Allow development to continue to happen in their respective repositories, sync the updated RBAC and CRD to include WAO's bundle, and publish a single operator from this repository.


## Layout

| Path | Contents |
| --- | --- |
| `operands.yaml` | Developer-selected source checkout paths, expected branches, and image repositories/tags |
| `api/v1alpha1/` | The `WorkloadAvailabilityConfig` API |
| `internal/controller/` | The lifecycle controller and default-config bootstrap |
| `internal/operands/` | Embedded runtime manifests and renderer |
| `config/operands/<key>/config/` | Complete copied upstream `config/` tree; replaced when refreshing that operand |
| `config/operands/<key>/overlays/{rbac,runtime}/` | WAO-owned packaging and runtime adjustments; retained when refreshing |
| `hack/update-operand-manifests.sh` | Local config copying and selected image synchronization |
| `hack/operand-image-refs.sh`, `hack/sync-operand-images.sh` | Offline validation of `operands.yaml` and `RELATED_IMAGE_*` synchronization into the manager Deployment |
| `hack/verify-operand-manifests.sh` | Generated consistency and RBAC/bundle shape checks |
| `bundle/` | Generated OLM bundle: one CSV, thirteen CRDs, operand RBAC |
| `config/catalog/`, `catalog/` | Catalog templates and rendered file-based catalog |

## Updating operand configuration

WAO copies each operand's `config/` tree and applies its own Kustomize overlays.
Refreshing the copy leaves WAO's adjustments intact. This follows OADP's direct-copy
[update targets](https://github.com/openshift/oadp-operator/blob/oadp-dev/Makefile).

See [operand configuration: copy, then split into two slices](docs/operand-config-overlays.md)
for why the copy is split into an install-time RBAC overlay and an enable-time runtime
overlay, and the [developer guide](docs/updating-operand-manifests.md) for setup, expected
file changes, and troubleshooting.

```sh
# Edit operands.yaml source paths/branches and image repositories/tags.
make update-nhc-manifests

# Prepare runtime objects for the WAO binary.
make operand-manifests

# Generate normalized bundle content when ready for packaging.
make bundle-reset
```

The three steps have separate responsibilities:

- **Copy:** `update-nhc-manifests`, `update-snr-manifests`, `update-sbr-manifests`,
  `update-far-manifests`, `update-mdr-manifests`, and `update-nmo-manifests` refresh one
  operand and synchronize its image settings. `update-operand-manifests` does all six
  sequentially. These targets neither render objects nor generate a bundle.
- **Render:** `operand-manifests` synchronizes `RELATED_IMAGE_*` in
  `config/manager/manager.yaml` and generates `internal/operands/manifests/<key>/objects.yaml`.
  The embedded objects carry `__WAO_<ENV>__:placeholder` image tokens that the operator
  replaces with its `RELATED_IMAGE_*` values at reconcile time. It needs no operand checkout.
- **Package:** `bundle` or `bundle-reset` generates the OLM bundle from copied CRDs
  and WAO RBAC overlays. Platform-specific adjustments belong in overlays/packaging,
  selected through `MANIFESTS_DIR`. This change retains the existing OpenShift
  integration; `bundle-k8s` is currently an alias, not a separate Kubernetes bundle.

Set checkout paths and branches for your machine in `operands.yaml` before copying:

```yaml
operands:
  nhc:
    source:
      path: ../node-healthcheck-operator
      branch: main
    images:
      RELATED_IMAGE_NHC_MANAGER:
        repository: quay.io/medik8s/node-healthcheck-operator
        tag: latest
      # NHC also selects its console image independently; see operands.yaml.
```

`source.path` names a Git repository root, relative to WAO's root or absolute.
Use an explicit path rather than `~` or `$VARIABLE`; YAML values have no shell
expansion. The expected branch must already be checked out. Missing paths,
detached HEAD, or a branch mismatch fail before replacing configuration; the helper
never fetches or switches branches. Copying includes uncommitted and untracked
files under `config/` and removes stale files from WAO's previous copy. Samples are
copied as source content; updating does not apply them or add them to WAO samples.

Each image has its own repository and tag, including the NHC console and SBR agent.
Tags such as `latest`, `wao`, and `v0.12.0` are used literally. The helper trusts your
source/image combination: it does not contact a registry, resolve digests, infer
source commits, or require an image to have been published. The runtime overlays are
static and need no edit when a tag changes. This does not add tag polling, automatic
rollouts, or different pull policies.

Supported Make builds (`manager`, `run`, `docker-build`, and the shared `dev-build`)
perform the render step before compiling WAO. They never refresh source checkouts.
Before bypassing Make with a direct `podman build` or `docker build`, run
`make operand-manifests` yourself; the Dockerfile consumes the prepared files.

Never hand-edit generated runtime objects. `make verify-operand-manifests` regenerates,
compares against the files it found, including uncommitted updates, restores them, and
reports any drift; it also checks RBAC and bundle shape. Bundle freshness is checked separately by
`make test` through `bundle-reset` and `verify-no-changes`.

## Development

Use the shared medik8s/tools targets to build and install WAO:

```sh
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-deploy DEV_CLUSTER_TYPE=external
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-undeploy DEV_CLUSTER_TYPE=external
```

The build includes your uncommitted WAO changes. External-cluster images use ttl.sh
with a short TTL (default two hours); redeploy if they expire. Development packaging
rewrites the bundle and manager image setting; run `make bundle-reset` before tests
or a commit. Every component keeps `enabled: false` after installation.

### Running tests
The current tests are simple Go unit and envtest suites, we do not have any e2e tests implemented.

If you want to run tests, run:
```sh
make test                     # Go unit and envtest suites
make test-offline             # verify-operand-manifests plus the operand pipeline regression tests (no cluster)
```

GitHub Actions runs the Go suites, lint, `make verify-operand-manifests` and the offline tests.

## Configure component resources

Set `spec.components.<key>.enabled` to `true` to enable a component.
The default is `false` for all six components.
Set `spec.components.<key>.manager.resources` to configure its manager CPU and memory.
Omitted resource values retain the packaged defaults.

Use the OpenShift console CSV details controls to edit `WorkloadAvailabilityConfig/cluster`.
Each component has an Enabled switch and Manager Resources controls.
WAO normally creates this singleton automatically.

See [Configure operand manager resources](docs/configuring-operand-resources.md)
for examples, console instructions, validation, and migration requirements.
The boolean API replaces `state` in `v1alpha1` and requires a controlled upgrade for existing installations.

## What Enabled and Disabled mean

**Enabled** (`enabled: true`) applies the component's operand objects with server-side apply: the
manager Deployment, Services, ConfigMaps and webhook configurations, rendered from the
copied upstream configuration (the `objects.yaml` embedded in the binary) with the selected
image references substituted. The operand ServiceAccounts, Roles and bindings are not part of
this: OLM creates them from the WAO CSV at install, and they exist whether or not the
component is Enabled. The phase becomes
`Available` once the managers are ready and, for NHC, SNR, FAR and NMO, the
service-ca CA bundles are injected into the webhooks.

**Disabled** (`enabled: false`, the default for every component) means WAO has not created, or has
removed, the operand objects it renders (not the OLM-owned RBAC). Specifically:

- Installing WAO creates no remediation policy, remediation request, native config,
  node agent, operand Deployment or webhook configuration. The CRDs are installed
  regardless, because the CSV owns them (D8).
- Disabling removes the component's webhook configurations along with the manager. A `Fail` webhook without a backend would block even deleting the component's objects, and WAO never switches a webhook to `Ignore`. While a component is disabled its objects are not validated.
  FAR is the exception: the WAO manager serves FAR admission in each state. See [FAR admission webhooks in WAO](docs/far-webhooks.md).
- Disable is **manager-level**. It does **not** stop what the component created
  itself: SNR's agent DaemonSet, default config, template and certificate Secret
  stay, as would SBR's agents and storage objects. WAO reports this as phase
  `DisableIncomplete` with the objects listed in `retainedResources`.
- Native objects that a disabled manager would reconcile are stranded: an active
  `NodeMaintenance` keeps its node cordoned, and FAR, MDR, SNR and SBR remediation
  objects keep their finalizers. Disable a component only when it has no active
  work.
- Objects the upstream managers create at runtime (NHC's aggregation ClusterRole,
  ConsolePlugin and the `medik8s-leases` namespace) are left behind by upstream,
  not by WAO. WAO never adopts or deletes objects it did not create.

## Uninstall

1. Set every component's `enabled` field to `false`.
   Wait for each phase to be `Disabled` or `DisableIncomplete`.
2. Delete the config: `oc delete workloadavailabilityconfig cluster`. Its finalizer
   removes any remaining operand objects. WAO creates a new all-Disabled config the
   next time it starts (D4), which is harmless.
3. Delete the Subscription and the CSV in `openshift-workload-availability`.

## Security notes

- Operand RBAC is not created by WAO. It ships in the WAO CSV (operand ServiceAccount
  permissions, via `--extra-service-accounts`) and as twenty-one standalone ClusterRoles in the
  bundle, and OLM creates it at install. Six operand ServiceAccounts are folded into the CSV; the
  SBR agent's RBAC (ServiceAccount `sbr-operator-sbr-agent`, ClusterRoles
  `sbr-operator-sbr-agent-role` and `sbr-operator-sbr-agent-privileged-scc`, and a
  ClusterRoleBinding) ships as standalone bundle objects instead, because the SBR manager creates
  ClusterRoleBindings to those two ClusterRoles by name at runtime and a folded role would get an
  OLM-generated name. The operand ServiceAccounts and roles therefore exist
  from install onward, even while every component is Disabled. WAO's own role holds no union of
  operand rules. The operand rules include:
  - SCC `use` on `privileged`, because the SNR and SBR CSVs grant it for their agents;
  - NMO's `oauth.openshift.io` wildcard;
  - cluster-wide `clusterroles` and `clusterrolebindings` management from NHC and SBR.
- For the `AllNamespaces` OperatorGroup, OLM promotes each CSV's namespaced `permissions`
  to cluster-wide roles and appends get/list/watch on `namespaces`. For FAR this means its
  manager can read Secrets (fence credentials) in every namespace, which upstream FAR requires.
- Enabling SNR starts privileged node agents on every eligible node.

## License

Apache License 2.0; see [LICENSE](LICENSE).
