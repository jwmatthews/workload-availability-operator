# Operand configuration: copy, then split into two slices

WAO installs six medik8s operators as operands without forking them. This page explains
how an operand's upstream `config/` tree becomes part of WAO: why it is copied verbatim,
why it cannot be shipped as-is, and how two Kustomize overlays split it into an
install-time slice for WAO's bundle and an enable-time slice that WAO applies only when a
component is enabled.

## Why copy the upstream config at all

Each operand's Deployment, Services, webhooks, RBAC and CRDs are defined by that operand's
own `config/` tree, and they change as the operand evolves. WAO does not want to re-author
them in Go or maintain a hand-edited copy: either would drift from upstream and violate the
hard constraint that component operators are consumed unmodified.

So `make update-<key>-manifests` copies the operand's entire `config/` directory from a
local checkout into `config/operands/<key>/config/`. The copy is verbatim and is replaced
wholesale on every refresh. Nothing under it is ever edited by WAO. See
[updating-operand-manifests.md](updating-operand-manifests.md) for the workflow.

## Why the copy cannot be shipped as-is

A copied `config/` is what the operand's own `make bundle` would package for a standalone
install. It contains:

| In the copied tree | What WAO must do with it |
| --- | --- |
| CRDs | Ship in WAO's bundle so OLM installs them with WAO |
| ServiceAccounts, Roles, bindings | Fold into WAO's CSV so OLM creates them at install |
| Manager Deployment, Services, ConfigMaps, webhook configurations | Do **not** create at install. Every component defaults to `Disabled`; WAO creates these only when the component is enabled |
| The operand's own CSV, Namespace, sample CR, scorecard config | Drop. Not WAO's to ship |

Two more things are missing from the upstream tree. A standalone OLM install would wire
OpenShift service-ca into the webhook Service, the Deployment's certificate mount and the
webhook configuration; WAO has to do that itself. And six managers in one namespace need a
component label on every pod and Service selector, or some upstream selectors match other
managers.

The copied tree therefore has to be split by *when* and *by whom* each object is created,
and adjusted for WAO's single-namespace, enable-on-demand model, all without touching the
copied files.

## The two overlays

Each operand has WAO-owned Kustomize overlays next to its copy:

```text
config/operands/<key>/
  config/                 # verbatim upstream copy; replaced on refresh, never edited
  overlays/
    rbac/                 # install-time slice: what goes into WAO's CSV
    runtime/              # enable-time slice: what WAO applies when the component is Enabled
```

Both overlays use the same copied packaging as their base (NHC uses its OpenShift overlay,
`config/manifests/ocp`; the others use `config/default`). They differ only in which kinds
they keep and which adjustments they apply. Overlays survive a refresh; the copy does not.

CRDs are the third slice but need no overlay: `config/operands/crd/kustomization.yaml`
lists the six copied `config/crd` directories, and WAO's `config/crd` includes it.

### Install-time slice: `overlays/rbac`

Keeps only ServiceAccount, Role, RoleBinding, ClusterRole and ClusterRoleBinding; every
other kind is deleted with a `$patch: delete` entry. Where upstream RBAC does not fit
WAO's packaging, a patch fixes the binding rather than the copied file. NHC's overlay, for
example, rebinds a kube-system Role to the operand's prefixed ServiceAccount so
operator-sdk folds it into the CSV instead of emitting a namespaced object OLM cannot
create in kube-system.

**How it is invoked.** `make bundle` runs `kustomize build config/manifests`, whose
kustomization lists all six `overlays/rbac` directories as resources, and pipes the result
into `operator-sdk generate bundle`. The operand ServiceAccounts are passed as
`--extra-service-accounts` (Makefile `OPERAND_SERVICE_ACCOUNTS`), so operator-sdk folds
the roles bound to them into the CSV's permissions. OLM creates all of it when WAO is
installed, whether or not any component is ever enabled.

`make verify-operand-manifests` checks that each rbac overlay emits only RBAC kinds and
that every operand ServiceAccount is listed in the Makefile.

### Enable-time slice: `overlays/runtime`

Applies WAO's integration adjustments on top of the copied base:

- `namespace: __WAO_NAMESPACE__`, a placeholder the operator replaces at reconcile time.
- `images:` entries that rewrite each upstream image to `__WAO_<ENV>__:placeholder`, also
  replaced at reconcile time with the `RELATED_IMAGE_<...>` value from the operator's
  environment. SNR's self-image and SBR's agent image are env values on the manager, so
  those operands carry a small patch setting the same token there.
- A `workloadavailability.medik8s.io/component` label on every Deployment pod template
  and Service selector.
- Patches wiring OpenShift service-ca into the webhook Service, the manager's certificate
  volume, and the webhook configuration's CA injection.
- FAR, NHC and NMO only: a patch that deletes the webhook configurations of the operand. The WAO
  manager serves the admission of these operands, and OLM installs those registrations with WAO.
  See [far-webhooks.md](far-webhooks.md), [nhc-webhooks.md](nhc-webhooks.md) and
  [nmo-webhooks.md](nmo-webhooks.md).

**How it is invoked.** `make operand-manifests` runs
`kustomize build config/operands/<key>/overlays/runtime`, filters the output to the kinds
in Makefile `RUNTIME_KINDS` (Deployment, Service, ConfigMap, Validating- and
MutatingWebhookConfiguration), and writes `internal/operands/manifests/<key>/objects.yaml`.
It fails if any `image:` field in that output is not a WAO placeholder, so an upstream
container WAO does not map can never deploy the upstream default. The file is embedded in
the WAO binary with `go:embed`.

At reconcile, when a component is `Enabled`, `internal/operands/render.go` loads the
embedded objects, substitutes the namespace and image placeholders, adds the managed-by
and component labels, orders Services before Deployments and webhooks last, and the
controller applies them with server-side apply. When the component is `Disabled` again,
the same set is removed. Nothing in this slice exists on the cluster until a component is
enabled.

## Where a change belongs

| You want to | Edit |
| --- | --- |
| Pick up upstream changes to an operand | Nothing by hand: `make update-<key>-manifests` replaces `config/operands/<key>/config/` |
| Change which image an operand runs | `operands.yaml`, then `make operand-manifests` |
| Fix how an operand is packaged into the CSV | `config/operands/<key>/overlays/rbac/` |
| Fix how an operand runs inside WAO's namespace | `config/operands/<key>/overlays/runtime/` |
| Edit a copied file | Never. The next refresh overwrites it. Patch it from an overlay instead |

If an upstream rename breaks a patch target, `kustomize build` fails during
`make operand-manifests` or `make bundle`. That failure is the intended signal to update
the overlay.
