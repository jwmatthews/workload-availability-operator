# Overview of Workload Availability Operator

The Workload Availability Operator (WAO) packages six medik8s operators as one
installation. It intends to simplify publishing and installation while preserving
the existing behavior.

This document explains the design and how to work with it. 

## Goal
Would like to see Medik8s 5.9 published as a single operator.
Need to work over next month to learn if there are any blockers that would prevent this.


## High Level Understanding
![WAO High Level](images/WAO_High_Level.svg)
 
### WAO is a single operator that manages operands

WAO treats the existing medik8s operators as **operands** which are managed by the WAO operator.

WAO is responsible for:
- **CRDs** - Collecting all of the operand CRDs and packaging so OLM will deploy.
  - This means that all of the operand CRDs are installed into the cluster at time of WAO installtion.
  - CRDs are NOT gated on the WAO operator's `enabled` state.
- **RBAC** - Collecting all of the operand RBAC related files and packaging in the WAO bundle so OLM will deploy on install of WAO.
- **Runtime manifests** - Understanding the runtime manifests for each operand and deploying them when the operand is `enabled`.
  - and...cleaning them up when the operand is `disabled`.

WAO is **not** responsible for the business logic of each operand.
- Each operand will continue to be developed in it's own repository.
- Operand changes are intended to be minimal to none
  - As of today we have not made any changes to any of the operands, yet it's likely will want to refactor the webhooks soon to allow them to be brought into the main WAO manager process.

### Recap of responsbilitlities: OLM - WAO - Operand
- **OLM**
  - On install of WAO
    - Deploys all CRDs
    - Deploys all RBAC
    - Deploys the WAO operator
- **WAO**
  - On enable
    - Deploys the operand's runtime manifests
  - On disable
    - Removes the operand's runtime manifests
- **Operand**
  - Responsible for reconciling it's CRs
  - Currently runs their own webhook server, we likely will want to move this to run in the main WAO manager process.

### What does install of WAO look like?

1. Install the WAO operator via OLM
2. The WAO operator creates the `WorkloadAvailabilityConfig` CR called `cluster` on bootstrap
    - Only one instance of a `WorkloadAvailabilityConfig` is allowed as per validation in the `WorkloadAvailabilityConfig` CRD
3. All operands are set to be disabled by default
4. User needs to enable operands 
5. After enabling operands, the WAO operator will reconcile the operand's runtime manifests
  6. It will create the Deployment, Service, ConfigMaps, and Webhooks of the operand.

#### How does a user enable/disable an operand?

Edit the `WorkloadAvailabilityConfig` CR called `cluster` to enable or disable an operand.

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
  webhooks. It preserves webhook failure policies and never changes `Fail` to
  `Ignore`. Objects created by an upstream controller can remain.

Disabling a component stops its manager. It also deletes SNR agent DaemonSets
in the install namespace and SBR agent DaemonSets across all namespaces, waiting
for the managers to stop before removing agents. Other objects created by the component (config CRs,
native API objects, certificates) can remain active or await that manager. WAO
reports detected retained objects in `status.components[].retainedResources`
with phase `DisableIncomplete`.
Complete active maintenance or remediation before disabling a component.
See [Enabled and Disabled behavior](../README.md#what-enabled-and-disabled-mean)
for the component-specific consequences.

See [/config/samples/workloadavailability_v1alpha1_workloadavailabilityconfig.yaml](/config/samples/workloadavailability_v1alpha1_workloadavailabilityconfig.yaml)
```yaml
apiVersion: workloadavailability.medik8s.io/v1alpha1
kind: WorkloadAvailabilityConfig
metadata:
  name: cluster
spec:
  components:
    nodeHealthCheck:
      enabled: false
    selfNodeRemediation:
      enabled: false
    storageBasedRemediation:
      enabled: false
    nodeMaintenance:
      enabled: false
    fenceAgentsRemediation:
      enabled: false
    machineDeletionRemediation:
      enabled: false
```
* For testing there is a script [hack/enable_all.sh](../hack/enable_all.sh)
* You can also tweak the memory and cpu limits and requests for each operand, [config/samples/workloadavailability_v1alpha1_workloadavailabilityconfig_resources.yaml](../config/samples/workloadavailability_v1alpha1_workloadavailabilityconfig_resources.yaml)

 
### How upstream configuration becomes a WAO release

WAO copies each upstream `config/` directory into this repository. Kustomize
overlays apply WAO's adjustments without editing the copied files.

```text
Copied upstream config/
  ├─ CRDs and RBAC overlay → OLM bundle
  └─ Runtime overlay → manifests embedded in the WAO binary


## Goal

Goal 

WAO branches and links 

Gists of example of commands 

Walk through manual update of an operand 

Walk through helper with operands.yaml 

Walk through bundle formation with overlays 
Walk through resource changes for limits 
Walk through objects yaml 


Walk through next steps 
Test
Upgrade scenario. 

Clean up 
Gap no webhooks until enable 
Objects yaml in golang 

Integration with Virt auto pilot

## Current Status
### Testing
- Testing is early, we've started to capture notes here:
   - https://github.com/medik8s/.github/wiki/wao-testing
- Beginning to setup automated tests starting with:
   - https://github.com/openshift/rhwa-system-tests/pull/112
### 'wao' branch
WAO is setup to track the `wao` branch of each operand's repo.
The `wao` branch started from each operand's`medik8s5.8` branch as of 11/2/2026


## Next Steps
1. Testing: Work to understand if there are blockers that prevent the goal of WAO for medik8s 5.9
2. Iron out the upgrade scenario 
2. Additional cleanup in WAO code:
    - Implement `bundle-k8s`, we currently only create a bundle for `ocp`
    - Consider: Bring webhooks to run in the WAO process so they are availabile and running even when a component is disabled.
    - Consider: Rewrite the internal/operands/manifests/<operand name>/objects.yaml in Go and stop trying to sync from the operand repo.
