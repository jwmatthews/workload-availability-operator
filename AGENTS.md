# Agent instructions

This repository is a proof of concept for a single Workload Availability Operator
(WAO) that installs the six medik8s operators (NHC, SNR, SBR, FAR, MDR and NMO)
as operands in one namespace.

## Hard constraints

- Do not modify, fork or rebuild the component operators. Consume the operand
  images as `repository:tag` selected in `operands.yaml`. This development helper
  keeps tags, trusts the selected source branch, and resolves no image digests
  or source revisions.
- Every component defaults to `Disabled`. Installing WAO must not create a
  remediation policy, remediation request, native config or node agent.
- Never change a component webhook `failurePolicy`, in particular never `Fail` to `Ignore`.
- WAO never adopts an existing object it did not create. It reports the conflict instead.
- No public API field may override a component image.

## Operand configuration edits

Read [the overlay guide](docs/operand-config-overlays.md) before changing operand packaging.
Use [the update guide](docs/updating-operand-manifests.md) for refresh and generation commands.

- Treat `config/operands/<key>/config/` as a copy of upstream configuration.
  Never edit these copied files directly. Refresh an affected copy with
  `make update-<key>-manifests`. Each refresh replaces the copied directory.
- Make WAO packaging adjustments in `config/operands/<key>/overlays/rbac/`.
- Make WAO runtime adjustments in `config/operands/<key>/overlays/runtime/`.
  Both overlay directories survive a refresh.
- Select operand images in `operands.yaml`. Run `make operand-manifests` to
  synchronize `RELATED_IMAGE_*` values in `config/manager/manager.yaml`.
- Regenerate `internal/operands/manifests/<key>/objects.yaml` with
  `make operand-manifests`. Never edit these generated runtime objects directly.
- Regenerate `bundle/` with `make bundle-reset` after changing its source configuration.
  Never edit generated bundle files directly.

## Cluster work

Live-cluster tests need a **disposable** OpenShift cluster with
classic OLM that you are authorized to modify.
 
The repository never contains cluster credentials or
cluster-identifying details. Do not run cluster tests unless the person you are
working for has confirmed which cluster to use in the current session.

- Always pass the kubeconfig explicitly; never rely on the current context. For
  the `make dev-*` targets that means `KUBECONFIG=<path> make <target>` on each
  command; they have no kubeconfig argument.
- Commit only sanitized summaries (`results.json`, `REPORT.md`) and only after review.
- Never capture Secret values.
- Use server-side dry-run for admission checks. Never create a real
  `NodeHealthCheck` policy, `StorageBasedRemediationConfig`, `NodeMaintenance` or
  remediation request (SNR, SBR, FAR, MDR) as part of the install tests. These
  objects reboot, fence, delete or drain real nodes.

### Build and install for development

Use the shared medik8s/tools targets. The former `hack/cluster-*.sh` scripts are
gone; do not recreate them.

```sh
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-deploy    # build, push, install with operator-sdk run bundle
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-undeploy  # operator-sdk cleanup --delete-all, which also deletes the owned CRDs and their custom resources
```

- The targets live in `dev/dev.mk` of medik8s/tools, found at `../tools` or set
  with `TOOLS_DIR`; otherwise the Makefile clones it into `.tools/` on first use.
- `dev-olm-deploy` builds the operator image from the working tree, so uncommitted
  changes are included. For a non-Kind cluster it pushes the operator and bundle
  images to ttl.sh with a short TTL (`TTL_SH_TTL`, default 2h); after that a pod
  restart cannot pull the image. Redeploy rather than debug `ImagePullBackOff`.
- The build rewrites `bundle/` and `config/manager/kustomization.yaml` with the
  dev image. Run `make bundle-reset` before `make test` or a commit, or
  `verify-no-changes` fails.
- Every component stays `Disabled` after install. Enable one by patching
  `workloadavailabilityconfig/cluster`; never create the operand policy objects
  listed above.

## Commits

Small commits per task.
Run `make check` before each commit (lint, import order, operand manifest
consistency; verification may regenerate stale operand files). Run
`make test-offline` and `make test` on a clean tree before pushing. Together they match what CI runs.
