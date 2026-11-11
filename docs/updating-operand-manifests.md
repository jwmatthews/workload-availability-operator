# Updating operand manifests

WAO copies an operand's local `config/` tree and applies its own Kustomize
overlays; [operand-config-overlays.md](operand-config-overlays.md) explains why. Choose the source and image tags in [operands.yaml](../operands.yaml),
refresh the copy, then generate runtime objects and package the bundle when ready.

Run these commands from the WAO repository root. You need Git, Bash, Make, Go,
and jq. Make installs yq, Kustomize, and packaging tools through their existing
dependency targets when needed.

## Quick start

```sh
# First edit operands.yaml: checkout paths, expected branches, image repositories/tags.

# Copy one operand's config and synchronize its image settings.
make update-nhc-manifests

# Generate runtime YAML from WAO's copied config.
make operand-manifests

# Review the changes.
git status --short
git diff

# Generate normalized bundle content when ready to package.
make bundle-reset

# Check consistency and run the offline regression tests.
make check
make test-offline
```

To refresh all six operands, use `make update-operand-manifests` in place of the
single-operand update. Configure all six checkout paths and branches first.

## Configure the source and images

Edit the existing entries in `operands.yaml`. For example, the NHC entry contains:

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
      RELATED_IMAGE_NHC_CONSOLE:
        repository: quay.io/medik8s/node-remediation-console
        tag: latest
```

Keep the other operand entries in the file. Set these values for your checkout:

- `source.path`: the operand's Git repository root. Relative paths resolve from
  WAO's root; absolute paths also work. Use explicit paths; `~` and `$VARIABLE`
  are not expanded inside YAML.
- `source.branch`: the branch already checked out in that repository. The helper
  checks the name and leaves the checkout unchanged. Uncommitted and untracked
  files under `config/` are included in the copy. Git worktrees are supported.
- `images.*.repository` and `tag`: the image to use, expressed separately. Tags
  such as `latest`, `wao`, or `v0.12.0` are used literally. Each image is selected
  independently, including the NHC console and SBR agent.

The runtime overlays under `config/operands/<key>/overlays/runtime/` are static: they
bake `__WAO_<ENV>__:placeholder` tokens into the generated objects, and the operator
replaces them with its `RELATED_IMAGE_*` values at reconcile time. A tag change never
edits them.

The helper trusts your source/image combination. Copying and rendering require
no published image, registry access, digest resolution, or source commit
discovery. The selected images must be available when you later run the operands.

If only image selections changed, run `make operand-manifests` directly. A source
refresh is needed when you want to copy updated configuration from a checkout.

## FAR inventory and debugging

The WAO Deployment includes an init container named `far-agent-inventory`.
It uses the FAR image selected in `operands.yaml`.
Image synchronization updates both this container and the manager's
`RELATED_IMAGE_FAR_MANAGER` value. The init container always checks the registry
when Kubernetes starts it.

The init container overrides the FAR entrypoint and lists `/usr/sbin`.
It does not start the FAR manager or execute a fence agent.
It writes two reports into a shared `emptyDir` volume:

- `/var/run/wao/far-inventory/usr-sbin.list` contains permissions, sizes, and symbolic link targets.
- `/var/run/wao/far-inventory/usr-sbin.names` contains one entry name per line.

These paths refer to the read-only mount in WAO's manager container.
The inventory includes directories and symbolic links, regardless of their names.
It does not establish whether each entry is an executable fence agent.
WAO does not yet read these reports or use them for admission checks.

Inventory runs for each new WAO Pod, including when the FAR component is disabled.
A manager container restart within the same Pod does not repeat inventory.
Replacing the Pod removes its reports.

WAO's manager starts only after inventory succeeds.
An image pull failure or inventory error prevents manager startup.
The init container prints the entry count and full listing to its logs.
Use these commands to inspect startup failures:

```sh
kubectl --kubeconfig /path/to/kubeconfig -n <wao-namespace> \
  describe pod <wao-pod>
kubectl --kubeconfig /path/to/kubeconfig -n <wao-namespace> \
  logs <wao-pod> -c far-agent-inventory
```

Use `--previous` with the log command after an init container restart.
The manager's default logs do not include init container output.

## Choose the update target

These targets each refresh one operand:

```sh
make update-nhc-manifests
make update-snr-manifests
make update-sbr-manifests
make update-far-manifests
make update-mdr-manifests
make update-nmo-manifests
```

For all operands, use:

```sh
make update-operand-manifests
```

The aggregate validates all selected inputs before replacement and processes the
six operands sequentially, including under parallel Make. Use it for an all-operand
refresh; individual update targets share the manager YAML file and should not be
invoked together with `make -j`.

## What to expect

An update prints a line for each copied operand, for example:

```text
== nhc: copying config/ from the selected working tree
```

The commands have separate outputs:

| Command | Expected changes |
| --- | --- |
| `make update-nhc-manifests` | Replace `config/operands/nhc/config/`; synchronize NHC `RELATED_IMAGE_*` values in `config/manager/manager.yaml` |
| `make update-operand-manifests` | Perform the same update for all six operands |
| `make operand-manifests` | Synchronize all `RELATED_IMAGE_*` values; generate `internal/operands/manifests/<key>/objects.yaml` |
| `make bundle-reset` | Generate and normalize `bundle/` from copied CRDs and WAO packaging configuration |

Refreshing a copy removes stale files deleted upstream. WAO-owned
`config/operands/<key>/overlays/{rbac,runtime}/` directories are retained and never
edited by an update.

Update targets stop after copying and image synchronization. Runtime generation
uses the configuration already copied into WAO and requires no operand checkout.
Neither step generates a bundle, applies resources to a cluster, or enables a
component. Repeating a command with identical inputs may produce no diff.

Supported Make build targets (`manager`, `run`, `docker-build`, and shared
`dev-build`) run `operand-manifests` before compiling. Before a direct
`podman build` or `docker build`, run `make operand-manifests` yourself.

## Review and troubleshoot

Review `operands.yaml`, copied configuration, image settings, generated runtime
objects, and any later bundle diff. Keep machine-specific checkout paths out of
shared changes unless they are intentional. Make integration adjustments in WAO's
overlays; a later refresh replaces files inside the copied `config/` directory.

`make verify-operand-manifests` regenerates in place, compares against the files it
found, restores them, and reports any drift; it also checks RBAC/bundle shape. Bundle freshness is checked separately by
`make test`, which requires a clean checkout; see [AGENTS.md](../AGENTS.md) for the
full commit/test workflow.

| Error | Action |
| --- | --- |
| Missing checkout, wrong Git root, or missing `config/` | Correct `source.path` to a local operand repository root containing `config/` |
| Branch mismatch or detached HEAD | Put the checkout on the intended branch yourself, or correct `source.branch` |
| Invalid image input or missing/duplicate image destination | Check the repository/tag fields; inspect the named `RELATED_IMAGE_*` entry in `config/manager/manager.yaml` |
| Kustomize failure, unreplaced placeholder, or an image field that is not a WAO placeholder | Check whether upstream files, resource names or image names changed and adjust WAO's overlays accordingly |

Input and branch checks fail before copied configuration is replaced. After
correcting an error, rerun the affected update or generation command. Review any
generated changes already present from an earlier attempt.
