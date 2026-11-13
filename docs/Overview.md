# Workload Availability Operator overview

The Workload Availability Operator (WAO) packages six medik8s operators as one
installation. This proof of concept tests a simpler way to distribute and manage
those operators. It preserves their existing behavior and upstream development.

This guide explains the design, then shows how to install WAO and enable one
component on a disposable OpenShift cluster.

## What WAO manages

An **operand** is a component that another operator manages. WAO treats each
medik8s operator as an operand, with its own controller process.

The components cover three tasks:

- **Detect unhealthy nodes:** Node Health Check (NHC) creates remediation requests
  according to a configured policy.
- **Remediate nodes:** Self Node Remediation (SNR), Storage Based Remediation
  (SBR), Fence Agents Remediation (FAR), and Machine Deletion Remediation (MDR)
  provide different remediation methods.
- **Maintain nodes:** Node Maintenance (NMO) cordons and drains a node when an
  administrator creates a maintenance request.

The [component table](../README.md)
links to each upstream project.

## How the parts work together

WAO separates package installation, component activation, and node operations.

| Stage | Responsible controller | Result |
| --- | --- | --- |
| Install WAO | Operator Lifecycle Manager (OLM) | Installs WAO, all component API definitions, and component permissions. |
| Enable a component | WAO | Creates that component's Deployments, Services, configuration, and webhooks. |
| Process a policy or request | The upstream component | Performs health checks, remediation, or maintenance through its existing API. |

OLM uses one WAO package instead of six independent component packages. Its
bundle contains the ClusterServiceVersion (CSV), which describes the installation,
and the CustomResourceDefinitions (CRDs), which define the APIs.

WAO creates one cluster-scoped `WorkloadAvailabilityConfig` named `cluster`.
Every component starts `Disabled`. An administrator changes a component's
`state` to `Enabled` to start its controller in WAO's installation namespace.

The WAO controller repeatedly compares the requested state with the cluster.
It applies the component resources and reports deployment and webhook readiness.
The component continues to process its own API objects independently.

**Enabling a component can have immediate effects.** For example, SNR starts
privileged node agents when enabled. NMO requires a separate `NodeMaintenance`
request before it drains a node. The trial below uses NMO.

## Why the design uses these boundaries

- **Keep the upstream operators unchanged.** WAO consumes published component
  images. Each project retains its controllers and APIs, while WAO provides one
  installation and upgrade lifecycle.
- **Let OLM install permissions.** OLM creates component service accounts and
  role-based access control (RBAC) resources during installation. WAO manages
  component workloads. Component permissions remain present when a component
  is disabled.
- **Start with every component disabled.** Installing WAO creates no component
  controller, remediation policy, remediation request, native configuration, or
  node agent. Administrators choose which components to activate.
- **Preserve resource ownership.** WAO reports a conflict if a required object
  already exists without WAO ownership. It does not adopt that object.
- **Make disabling explicit.** WAO removes the resources it manages, including
  webhooks. The FAR and NHC webhooks are an exception: the WAO manager serves them in each
  state, as [FAR admission webhooks in WAO](far-webhooks.md) and
  [NHC admission webhooks in WAO](nhc-webhooks.md) describe. It preserves webhook failure policies and never changes `Fail` to
  `Ignore`. Objects created by an upstream controller can remain.

Disabling a component stops its manager, but existing agents or native API
objects can remain active or await that manager. WAO reports detected retained
objects in `status.components[].retainedResources` with phase `DisableIncomplete`.
Complete active maintenance or remediation before disabling a component.
See [Enabled and Disabled behavior](../README.md#what-enabled-and-disabled-mean)
for the component-specific consequences.

### How upstream configuration becomes a WAO release

WAO copies each upstream `config/` directory into this repository. Kustomize
overlays apply WAO's adjustments without editing the copied files.

```text
Copied upstream config/
  ├─ CRDs and RBAC overlay → OLM bundle
  └─ Runtime overlay → manifests embedded in the WAO binary
```

The overlays separate installation resources from resources needed when a
component starts. They also provide namespace settings, component selectors,
and OpenShift service-ca integration for webhook certificates.

Developers select image repositories and tags in [operands.yaml](../operands.yaml).
WAO substitutes those image references into its embedded manifests at runtime.
The public configuration API has no image override. The current development
workflow trusts the selected tags and source branches without resolving image
digests or source revisions.

See [operand configuration overlays](operand-config-overlays.md) for the design.
See [updating operand manifests](updating-operand-manifests.md) for the commands.

## Try the component lifecycle

### 1. Prepare

Use a disposable OpenShift cluster that you are authorized to modify. It must
have classic OLM and the OpenShift service-ca controller. Use a cluster without
existing installations of the six component operators.
The cluster must be able to pull the images selected in [operands.yaml](../operands.yaml).

On your workstation, you need:

- Git, Bash, Make, curl, and jq.
- Go matching [go.mod](../go.mod).
- The OpenShift `oc` client and an explicit kubeconfig with cluster-admin access.
- A running Podman engine. For Docker, add `CONTAINER_TOOL=docker` to the Make commands.

Run the commands from the WAO repository root in the same shell. Set the path
to your disposable cluster's kubeconfig:

```sh
WAO_KUBECONFIG=/path/to/disposable.kubeconfig
```

The build uses the copied manifests already in this repository. You do not need
the six upstream checkouts unless you want to refresh those copies.

### 2. Install WAO

```sh
KUBECONFIG="$WAO_KUBECONFIG" make dev-olm-deploy DEV_CLUSTER_TYPE=external

oc --kubeconfig="$WAO_KUBECONFIG" wait --for=create \
  workloadavailabilityconfig/cluster --timeout=120s

oc --kubeconfig="$WAO_KUBECONFIG" get workloadavailabilityconfig cluster -o yaml
```

The target builds your working tree, including uncommitted changes. It publishes
development images to ttl.sh and installs the bundle through OLM. These images
expire after two hours by default. Repeat the deployment if they expire.

The Makefile uses shared [medik8s/tools targets](https://github.com/medik8s/tools/blob/main/dev/dev.mk).
It uses `../tools` when available, or downloads the tools into `.tools/`.
Make installs the required generation and packaging tools when needed.

Expect all six component states and phases to become `Disabled`.
The `Available=True` condition means WAO completed reconciliation. It does not
mean that workloads have protection.

### 3. Enable Node Maintenance

This step starts the NMO controller. **Do not create a `NodeMaintenance` request
for this trial.** Such a request would cordon and drain a real node.

```sh
oc --kubeconfig="$WAO_KUBECONFIG" patch workloadavailabilityconfig cluster \
  --type=merge -p '{"spec":{"components":{"nodeMaintenance":{"state":"Enabled"}}}}'

oc --kubeconfig="$WAO_KUBECONFIG" wait workloadavailabilityconfig/cluster \
  --for='jsonpath={.status.components[?(@.name=="nodeMaintenance")].phase}=Available' \
  --timeout=300s

oc --kubeconfig="$WAO_KUBECONFIG" get deployments \
  -n openshift-workload-availability \
  -l workloadavailability.medik8s.io/component=nodeMaintenance
```

Expect the NMO Deployment to become ready. Its component phase becomes
`Available` after the deployment is ready and its webhook CA bundle is present.
The other five components remain disabled.

If the wait fails, inspect the configuration status:

```sh
oc --kubeconfig="$WAO_KUBECONFIG" get workloadavailabilityconfig cluster -o yaml
```

Check the component's `message`, `deployments`, and `admissionReady` fields.
`Progressing` means WAO is waiting for readiness. `Degraded` reports a
reconciliation error, such as an ownership conflict.

### 4. Disable NMO and remove the trial installation

```sh
oc --kubeconfig="$WAO_KUBECONFIG" patch workloadavailabilityconfig cluster \
  --type=merge -p '{"spec":{"components":{"nodeMaintenance":{"state":"Disabled"}}}}'

oc --kubeconfig="$WAO_KUBECONFIG" wait workloadavailabilityconfig/cluster \
  --for='jsonpath={.status.components[?(@.name=="nodeMaintenance")].phase}=Disabled' \
  --timeout=300s
```

Expect WAO to remove the NMO Deployment and webhook configuration.
If the phase becomes `DisableIncomplete`, inspect `retainedResources` before
continuing. Follow the [uninstall guidance](../README.md#uninstall) if you enabled
other components or created native API objects.

After the wait succeeds, remove the configuration while WAO still runs:

```sh
oc --kubeconfig="$WAO_KUBECONFIG" delete workloadavailabilityconfig cluster

KUBECONFIG="$WAO_KUBECONFIG" make dev-olm-undeploy DEV_CLUSTER_TYPE=external

make bundle-reset
```

The development cleanup leaves CRDs installed. `bundle-reset` restores normalized
local bundle content after the development build changes it. Run it before tests
or a commit.

## Continue from here

- **Change an operand version or configuration:** use the
  [update guide](updating-operand-manifests.md).
- **Read the controller:** start with the
  [configuration API](../api/v1alpha1/workloadavailabilityconfig_types.go), then the
  [lifecycle controller](../internal/controller/workloadavailabilityconfig_controller.go)
  and [manifest renderer](../internal/operands/render.go).
- **Contribute a change:** see [development and tests](../README.md#development)
  and the repository [contributor instructions](../AGENTS.md).
