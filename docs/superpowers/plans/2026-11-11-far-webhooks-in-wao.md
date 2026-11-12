# FAR Webhooks in WAO Implementation Plan

> **For agentic workers:** Use `superpowers:executing-plans` to execute this plan task by task. Use checkboxes to record progress. Do not start implementation without a separate instruction to execute the plan.

**Goal:** Serve FAR admission from the WAO manager, including when the FAR operand is Disabled.

**Architecture:** Import FAR's public validators and copy only its inaccessible internal webhook files. A WAO init container inspects the selected FAR image. WAO loads the resulting inventory before it serves admission. Classic OLM supplies WAO certificates and installs the four webhook entries.

**Tech stack:** Go 1.26.5, controller-runtime v0.24.1, Kubernetes modules v0.36.3, Kustomize, Operator SDK, classic OLM, Ginkgo, Gomega, and envtest.

**Spec:** The decisions and acceptance criteria in this document form the implementation specification. They reproduce the discussion dated 2026-11-11. The coding agent does not need untracked notes or private research. Also read [AGENTS.md](../../../AGENTS.md), [the overlay guide](../../operand-config-overlays.md), and [the update guide](../../updating-operand-manifests.md).

**Status:** Plan only. The dependency update, imported handlers, and full admission workflow have not been implemented or tested. The inventory experiment described below is complete.

**Decisions (2026-11-12):** The agent inventory is a name-only list (Task 3). The Secret logging concern is documented only, and its fix is outside this work (Task 4). WAO runs 2 replicas (Task 7).

## Global constraints

- Preserve `minKubeVersion: 1.29.0` and WAO's existing Common Expression Language (CEL) rules.
- Every operand defaults to `Disabled`.
- An all-Disabled installation must create no native config, policy, remediation request, maintenance request, or node agent.
- Preserve webhook `failurePolicy: Fail`. Never use `Ignore` to make a test pass.
- Never adopt foreign resources. Report conflicts without changing those resources.
- No public API field may override an operand image.
- Select operand images as `repository:tag` through `operands.yaml` and `RELATED_IMAGE_*`.
- Import public FAR code. Copy only inaccessible internal webhook files and relevant tests. Preserve their licenses.
- Do not copy FAR controllers, parameter validators, template libraries, or the CLI executor into WAO.
- Do not edit copied files under `config/operands/<key>/config/` directly.
- Put FAR runtime adjustments in `config/operands/far/overlays/runtime/`.
- Generate embedded runtime objects with `make operand-manifests`. Generate the bundle with `make bundle-reset`.
- Keep the dependency update separate from FAR integration. Check existing behavior before adding FAR imports.
- Make the agreed FAR flag change only after basic FAR admission works in WAO.
- Do not modify other operands. The later FAR flag is a narrow approved exception to the operand source restriction.
- Use `gh` for GitHub access. Draft every proposed GitHub write and obtain approval for that exact action first.
- This plan does not authorize a push, issue write, pull request, image publication, or cluster installation.
- Preserve unrelated working files. Never reset, clean, or stash another person's work without permission.
- Preserve configured Git identities. Append `Co-authored-by: Codex <noreply@openai.com>` exactly once when committing assisted changes.

## Scope and sequence

| Stage | Deliverable | Required predecessor |
| --- | --- | --- |
| A | Separate dependency update | Baseline checks |
| B | FAR source pin and compatibility decisions | A |
| C | Local inventory, Secret logging documentation, and handler tests | B |
| D | WAO startup, packaging, lifecycle, and envtest | C |
| E | Basic classic OLM acceptance with FAR Disabled | D and explicit cluster authorization |
| F | FAR disabling flag and WAO packaging cleanup | Successful E |
| G | Desired OLM v1 checks | Required classic OLM checks |

Tasks 3 and 4 contain no open decision gate. The user approved a name-only inventory on 2026-11-12.
The user also moved the Secret logging fix outside this work. Task 4 only documents that concern.
Ask the user when a result needs a behavior change, an external source change, or a changed implementation sequence.

### Release scope

Automated upgrades, adoption, migration, legacy cleanup, and automated rollback are outside this release.
The release replaces six separately installed operators with WAO through a manual maintenance window:

1. Back up the medik8s custom resources (CRs).
2. Uninstall all medik8s operators.
3. Install WAO with all operands Disabled.
4. Enable the required operands.
5. Recreate their associated CRs.

Document this procedure. Do not execute it as an acceptance test.
Future WAO releases will address upgrades.

## Behavior to preserve

### Admission entries

Use group `fence-agents-remediation.medik8s.io` and version `v1alpha1` for every entry.
Register only `CREATE` and `UPDATE`. Imported `ValidateDelete` methods do not justify a DELETE registration.
Preserve AdmissionReview `v1`, `sideEffects: None`, and all other effective upstream settings.

| Entry name | Type | Resource | Path |
| --- | --- | --- | --- |
| `mfenceagentsremediation.kb.io` | Mutating | `fenceagentsremediations` | `/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation` |
| `vfenceagentsremediation.kb.io` | Validating | `fenceagentsremediations` | `/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation` |
| `mfenceagentsremediationtemplate.kb.io` | Mutating | `fenceagentsremediationtemplates` | `/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate` |
| `vfenceagentsremediationtemplate.kb.io` | Validating | `fenceagentsremediationtemplates` | `/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate` |

Classic OLM may generate admission configuration names. Tests must locate entries by their rules and entry names.
Do not assume that OLM preserves the two legacy operand configuration names.

### Defaulting and validation

- Preserve the legacy Secret name `fence-agents-credentials-shared`.
- Preserve the distinction between a nil Secret name and a pointer to an empty string.
- Preserve CREATE versus UPDATE defaulting based on `CreationTimestamp`.
- Add `remediation.medik8s.io/multiple-templates-support: "true"` only when the template annotation is absent.
- Preserve existing annotation values, including `"false"`.
- Preserve the extra template UPDATE check for removal of the old default Secret reference while that Secret exists.
- Preserve strategy checks, parameter rendering, parameter precedence, duplicate detection, and action rules.
- Preserve the current shared-only parameter condition. Do not correct suspected behavior without a separate decision.
- Accept an agent only if its exact name is in the inventory. Task 3 lists the approved differences from the FAR checker.
- Do not impose executable or regular-file restrictions.
- Draft separate issues for desired behavior changes. Obtain approval before creating those issues.

### Startup, readiness, and lifecycle

- Initialize FAR admission on every WAO replica, including replicas that do not lead reconciliation.
- Initialize imported package globals once, before concurrent requests start.
- An invalid inventory, failed version discovery, or failed registration must stop the whole manager, even with FAR Disabled.
- Run 2 WAO replicas. Each replica serves admission. Only the leader reconciles.
- Missing certificates or a listener that does not serve must prevent WAO readiness.
- Check the local webhook listener. Do not require the FAR Deployment or WAO's own Service endpoints for Pod readiness.
- Central admission belongs to the WAO installation. It must have no `WorkloadAvailabilityConfig` owner reference.
- FAR enable, disable, or `WorkloadAvailabilityConfig` deletion must not remove central admission.
- WAO uninstall must remove its central admission registrations.

## File responsibilities

These paths describe planned files. Some do not exist yet.

| File or directory | Responsibility |
| --- | --- |
| `go.mod`, `go.sum`, `Makefile`, `.github/workflows/{test,lint}.yml`, `Dockerfile` | Dependency and tool alignment |
| `internal/webhooks/far/SOURCE.md` | FAR module pin, source revision, copied files, licenses, adaptations |
| `internal/webhooks/far/v1alpha1/fenceagentsremediation_webhook.go` | Copied FAR defaulter and adapted setup |
| `internal/webhooks/far/v1alpha1/fenceagentsremediationtemplate_webhook.go` | Copied template defaulter and shared Secret defaulting |
| `internal/webhooks/far/setup.go` | Initialize FAR package state and register both resources |
| `internal/webhooks/far/client.go` | Construct a dedicated uncached admission client |
| `internal/webhooks/far/inventory/inventory.go` | Load the checked name list and return an immutable agent checker |
| `internal/webhooks/far/inventory/inventory_test.go` | Parser and lookup tests |
| `internal/webhooks/far/suite_test.go`, `admission_test.go` | Real registered handlers against envtest |
| `cmd/main.go` | Scheme, discovery, inventory, client, handlers, and readiness |
| `internal/components/far.go` | Remove operand-owned admission declarations |
| `internal/controller/workloadavailabilityconfig_controller.go` | Report central FAR admission without changing other operands |
| `config/manager/manager.yaml` | 2 replicas, inventory init container, mounts, arguments, and serving port |
| `hack/{operand-image-refs,sync-operand-images,verify-operand-manifests}.sh` | Check and synchronize both FAR image destinations |
| `config/operands/far/overlays/runtime/` | Remove legacy registrations, then later remove unused certificate resources |
| `config/manifests/bases/workload-availability-operator.clusterserviceversion.yaml` | Four installation-level webhook definitions |
| `test/offline/far_admission_test.go` | Bundle rules, ownership boundary, image synchronization, and RBAC |
| `docs/far-webhooks.md` | Contributor checks, operational behavior, inventory differences, Secret logging concern, and limitations |

## Task 1: Check the baseline and update dependencies separately

**Files:** Modify `go.mod`, `go.sum`, `Makefile`, `.github/workflows/test.yml`, and `.github/workflows/lint.yml` as required. Inspect `Dockerfile`. Update tracked vendor files only if the repository tracks them. Limit source edits to required compatibility changes.

**Consumes:** Existing WAO behavior, without FAR imports.

**Produces:** An independently reviewed dependency change with the exact targets below.

| Dependency or tool | Target |
| --- | --- |
| Go directive | `1.26.0` |
| Toolchain | `go1.26.5` |
| controller-runtime | `v0.24.1` |
| Required Kubernetes modules | `v0.36.3` |
| Ginkgo | `v2.32.1` |
| Gomega | `v1.42.1` |
| `k8s.io/utils` | `v0.0.0-20260707023825-cf1189d6abe3` |
| `sigs.k8s.io/yaml` | `v1.6.0` |
| controller-gen | `v0.21.0` |
| Default envtest assets | `1.36` |

All six checked operand manifests use Go 1.26.0, toolchain 1.26.5, and controller-runtime v0.24.1.
FAR uses the listed newer Kubernetes, Ginkgo, and Gomega versions. The other five use Kubernetes v0.36.2.
Some FAR indirect Kubernetes requirements remain v0.36.2. Align WAO's required Kubernetes modules at v0.36.3.
This comparison does not establish Kubernetes 1.29 server compatibility.

- [ ] Read repository instructions. Record `git status --short`, the current revision, and relevant tool versions.
- [ ] Create an isolated implementation checkout if needed. Preserve existing untracked files. Do not depend on them for execution.
- [ ] Run the existing checks before editing. Separate baseline failures from new failures.

```sh
go version
go list -m
make check
make test-offline
make test
```

`make test` requires a clean tracked tree. Do not use a reset to obtain one.

- [ ] Set the Go directives and direct requirements.

```sh
go mod edit -go=1.26.0 -toolchain=go1.26.5
GOTOOLCHAIN=go1.26.5 go get \
  github.com/onsi/ginkgo/v2@v2.32.1 \
  github.com/onsi/gomega@v1.42.1 \
  k8s.io/api@v0.36.3 k8s.io/apimachinery@v0.36.3 \
  k8s.io/client-go@v0.36.3 \
  k8s.io/apiextensions-apiserver@v0.36.3 \
  k8s.io/apiserver@v0.36.3 k8s.io/component-base@v0.36.3 \
  k8s.io/utils@v0.0.0-20260707023825-cf1189d6abe3 \
  sigs.k8s.io/controller-runtime@v0.24.1 sigs.k8s.io/yaml@v1.6.0
```

- [ ] Align other required Kubernetes modules if dependency selection introduces them. Do not assign v0.36.3 to independently versioned modules.
- [ ] Set controller-gen and default envtest assets in `Makefile` to the table values.
- [ ] Check setup-envtest and golangci-lint against Go 1.26.5. Select supported releases from official versioned documentation or source.
- [ ] Record the selected tool releases in the change description. Keep Makefile and CI linter versions identical.
- [ ] Correct `LINT_GOTOOLCHAIN` if it selects Go 1.26.0 instead of the approved patch version.
- [ ] Check CI and container builds use Go 1.26.5. `GOTOOLCHAIN=auto` alone does not prove the selected version.
- [ ] Run `go mod tidy`. Check the selected graph with `go list -m all`.
- [ ] Generate manifests and the normalized bundle. Review all API, CEL, RBAC, and bundle differences.

```sh
make manifests generate
make operand-manifests
make bundle-reset
make check
make test-offline
make test-no-verify-changes
go build ./cmd/...
```

- [ ] Check existing CRDs and controller tests with assets `1.29`. Use the exact asset path.
- [ ] Check `internal/controller/suite_test.go` honors `KUBEBUILDER_ASSETS` before its fallback directory search. Correct this test setup if necessary.

```sh
make envtest
KUBEBUILDER_ASSETS="$(bin/setup-envtest use '1.29.x!' -p path --bin-dir testbin)" \
  go test ./internal/controller/... -count=1
```

Check the actual setup-envtest executable path in `Makefile` before running this command.
If assets cannot run on the host, use Linux CI or a local Linux container. Do not count a skipped test as success.

- [ ] Review this dependency change separately. Do not add FAR imports to this change.
- [ ] Run `make check` before its local commit. Run `make test-offline` and `make test` on a clean committed checkout before any approved push.

**Acceptance:** Existing behavior passes with the new dependencies on the default assets and Kubernetes 1.29. The CSV minimum remains 1.29.0.

## Task 2: Pin FAR and preserve the import boundary

**Files:** Modify `go.mod` and `go.sum`. Create `internal/webhooks/far/SOURCE.md` and the two planned files under `internal/webhooks/far/v1alpha1/`. Create adjacent adapted upstream test files.

**Source reference:** Checked FAR branch `wao`, revision `58e8107d32e51ca28f285c786ed7a9c18b193a25`.
Its public module path is `github.com/medik8s/fence-agents-remediation/v5`.
The current image selection is `quay.io/medik8s/fence-agents-remediation-operator:wao`.
Neither the branch nor the image tag establishes a reproducible Go module pin.

- [ ] Check the local FAR revision and its `go.mod`. Resolve a published version or commit that contains the reviewed public APIs.

```sh
git -C ../fence-agents-remediation rev-parse HEAD
git -C ../fence-agents-remediation status --short
go list -m -json github.com/medik8s/fence-agents-remediation/v5@58e8107d32e51ca28f285c786ed7a9c18b193a25
```

- [ ] If the revision cannot resolve under the canonical module, report that result. Propose a resolvable source pin for review.
- [ ] Do not commit a local-path replacement or silently substitute another revision. Recheck behavior when a different revision is selected.
- [ ] Add the reviewed requirement. Check module downloads work without the sibling checkout.
- [ ] Import the API types and `pkg/validation`. Permit normal transitive imports such as `medik8s/common` and FAR's public template helpers.
- [ ] Copy these two source files with their license headers:

```text
internal/webhook/v1alpha1/fenceagentsremediation_webhook.go
internal/webhook/v1alpha1/fenceagentsremediationtemplate_webhook.go
```

- [ ] Adapt only setup wiring so both setup functions receive the dedicated client:

```go
func SetupFARWebhookWithManager(mgr ctrl.Manager, admissionClient client.Client) error
func SetupFARTemplateWebhookWithManager(mgr ctrl.Manager, admissionClient client.Client) error
```

Pass `admissionClient` to both imported validators and copied defaulters. Preserve the defaulting bodies and webhook markers.

- [ ] Adapt the corresponding upstream tests. Use WAO test initialization. Do not copy FAR's manager startup or controllers.
- [ ] Record the source URL, revision, resolved module version, copied tests, and each adaptation in `SOURCE.md`.
- [ ] Compile the copied package without registering it in `cmd/main.go`.

```sh
go test ./internal/webhooks/far/v1alpha1 -count=1
go mod verify
```

**Acceptance:** The code imports public validation. The copied code has a short, reviewable difference from the recorded source.

## Task 3: Build the name-only agent inventory

**Files:** Create `internal/webhooks/far/inventory/inventory.go` and `inventory_test.go`. Record the known differences in `docs/far-webhooks.md`.

**Decision (2026-11-12):** The user approved a name-only inventory. WAO accepts an agent only if its exact name is in the list.
WAO does not reproduce FAR's `os.Stat(filepath.Join("/usr/sbin/", agent))` behavior for inputs that are not plain names.

**Consumes:** A name list that the init container from Task 7 writes from the selected FAR image.

**Produces:** An immutable `validation.AgentExists` function through this WAO interface:

```go
func Load(path string) (validation.AgentExists, error)
```

### Inventory format

- The file is UTF-8 text with one agent name on each line. Each line ends with a newline character.
- The packaged path is `/var/run/wao/far-agent-inventory/agents.txt`.
- Each name must match `^fence_[^/\s]+$` and contain no more than 255 bytes.
- The file must contain between 1 and 1024 names. It must contain no duplicate names and no empty lines.
- The file contains no image reference and no architecture. It is a snapshot from the start of the WAO Pod.

### Collector command

The init container in Task 7 runs this command. The selected FAR image uses CentOS Stream 9 and supplies `sh`, `ls`, and `mv`.

```sh
sh -c 'set -eu
out=/var/run/wao/far-agent-inventory
cd /usr/sbin
ls -1d -- fence_* > "$out/agents.txt.tmp"
mv "$out/agents.txt.tmp" "$out/agents.txt"'
```

The command fails if no name matches. The rename makes the final file complete or absent.
The command reads directory entries only. It does not execute an agent or read file contents.

### Experiment evidence

An inspection-only init container ran locally and on a temporary OpenShift Pod on Linux amd64.
The image contained 31 matching names. All were executable regular files. No matching symlink existed.
All 31 names matched FAR's imported checker.

### Known differences from the FAR checker

Record this table in `docs/far-webhooks.md`. These differences are approved.

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

- [ ] Write `inventory_test.go` first. Cover each row that an offline fixture file can represent.
- [ ] Implement `Load`. Reject a missing, unreadable, empty, or oversized file.
- [ ] Reject an invalid name, a duplicate name, an empty line, and a file without a final newline.
- [ ] Return the checker only after all checks pass. The checker returns `(false, nil)` for each name that is not in the list.
- [ ] Keep the data immutable while WAO serves. Check concurrent lookups with the race detector.
- [ ] Check that an unlisted name gives FAR's public "unsupported fence agent" validation message through the imported validator.
- [ ] Do not add registry access or digest resolution to `make operand-manifests`. Keep all inventory tests offline.

```sh
go test ./internal/webhooks/far/inventory -count=1
go test -race ./internal/webhooks/far/inventory -count=1
```

**Acceptance:** `Load` accepts only a complete, valid name list. The checker accepts exact listed names only. The documentation lists the known differences.

## Task 4: Document the Secret logging concern

**Files:** Add a section to `docs/far-webhooks.md`.

**Tracker:** [Issue 2: Prevent FAR admission from logging Secret-derived action values](https://github.com/jwmatthews/workload-availability-operator/issues/2).

**Decision (2026-11-12):** A fix for this concern is outside the scope of this work. This plan adds no WAO logging adapter and no FAR logging change.
The concern does not block admission activation.

FAR's `api/v1alpha1/fenceagentsremediation_params.go` contains this log statement in `validateFenceAction`:

```go
paramsLog.Error(err, "can't build CR with this action attribute", "action", paramVal)
```

`paramVal` can come from a Secret. The statement runs only when the action value is not empty, `reboot`, or `off`.
Standalone FAR writes this value to the FAR manager log. After this work, the same value appears in the WAO manager log.

- [ ] Document the log statement, its condition, and the log that receives the value.
- [ ] Document that WAO reads Secrets for admission with cluster-wide `get` permission.
- [ ] Link Issue 2 as the tracker for a later fix. Do not write to the issue without approval.
- [ ] Use only synthetic Secret values in tests and on clusters. Never read, copy, or log production Secret values.

**Acceptance:** The contributor documentation describes the concern and links its tracker.

## Task 5: Adapt defaulters and preserve validation behavior

**Files:** Complete `internal/webhooks/far/v1alpha1/` and its adapted test files.

**Consumes:** Imported FAR validators and an injected `client.Client`.

**Produces:** The two setup functions from Task 2, each registering a validator and defaulter with the same dedicated client.

- [ ] Keep the imported validators unchanged. Retain both copied defaulter bodies and the shared defaulting helper.
- [ ] Add this defaulting table for both resource types:

| Operation | Reference before mutation | Legacy Secret | Expected reference |
| --- | --- | --- | --- |
| CREATE | nil | exists | legacy name |
| CREATE | nil | absent | nil |
| UPDATE | nil | exists | nil |
| CREATE or UPDATE | legacy name | absent | nil |
| CREATE or UPDATE | legacy name | exists | legacy name |
| CREATE or UPDATE | custom name | either | unchanged |
| CREATE or UPDATE | empty string pointer | either | unchanged |
| Required Secret check | nil or legacy name | Forbidden or transport error | return error |

- [ ] Check template annotations with nil annotations, an absent key, existing `"true"`, and existing `"false"`.
- [ ] Check template UPDATE with old legacy reference, removed new reference, and the legacy Secret present or absent.
- [ ] Check that ordinary FAR UPDATE retains its existing behavior instead of acquiring the template-only restriction.
- [ ] Use a client spy to prove every Secret key uses the admitted resource's namespace.
- [ ] Preserve imported checks for supported and unsupported strategies, malformed parameter templates, and missing node parameters.
- [ ] Check shared and node Secret parameters, rendering with `NodeName`, duplicates, and Secret/CR collisions.
- [ ] Check `action` and `--action` with empty, `reboot`, `off`, and invalid values.
- [ ] Check the shared-only parameter condition against the pinned FAR source. Record desired changes separately.

Use this safe base object in local tests. It must never become a real cluster request:

```go
func validFAR() *farapi.FenceAgentsRemediation {
    return &farapi.FenceAgentsRemediation{
        ObjectMeta: metav1.ObjectMeta{Name: "wao-admission-test", Namespace: "far-test"},
        Spec: farapi.FenceAgentsRemediationSpec{
            Agent: "fence_ipmilan",
            RemediationStrategy: farapi.ResourceDeletionRemediationStrategy,
            NodeParameters: map[farapi.ParameterName]map[farapi.NodeName]string{
                "--ipport": {"worker-test": "6230"},
            },
        },
    }
}
```

- [ ] Run the tests before changing behavior. Check that new integration tests detect the relevant missing wiring.
- [ ] Complete the minimal adaptation. Run the same cases again.

```sh
go test ./internal/webhooks/far/v1alpha1 -count=1
```

**Acceptance:** Defaulting and validation match the pinned FAR implementation, except for the approved inventory differences in Task 3.

## Task 6: Initialize admission and readiness in every WAO replica

**Files:** Create `internal/webhooks/far/setup.go`, `client.go`, and focused tests. Modify `cmd/main.go` and `config/rbac/role.yaml` through RBAC generation.

**Planned interfaces:**

```go
type Options struct {
    AgentExists validation.AgentExists
    AdmissionClient client.Client
    OutOfServiceTaintSupported bool
}

func SetupWithManager(mgr ctrl.Manager, opts Options) error
func NewAdmissionClient(cfg *rest.Config, scheme *runtime.Scheme) (client.Client, error)
```

Use package alias `farwebhooks` in `cmd/main.go`. Use `farapi` for the imported API.
Do not register FAR controllers or execute agents through these interfaces.

- [ ] Register `farapi.AddToScheme(scheme)` once in WAO's scheme setup.
- [ ] Add `--far-agent-inventory`, with packaged path `/var/run/wao/far-agent-inventory/agents.txt`.
- [ ] Load inventory with `inventory.Load` before handler registration.
- [ ] Discover strategy support through `validation.NewOutOfServiceTaintValidator(mgr.GetConfig())`.
- [ ] Return a startup error for discovery failure. Do not default to an unsupported strategy flag and continue.
- [ ] Construct the admission client with `client.New(cfg, client.Options{Scheme: scheme})`.
- [ ] Pass that client to validators and defaulters. Do not use `mgr.GetClient()` for admission Secret reads.
- [ ] Do not substitute `mgr.GetAPIReader()`. The imported validator requires the full `client.Client` interface.
- [ ] Add a Secret RBAC marker to the admission integration package:

```go
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
```

- [ ] Generate RBAC and check WAO's effective permissions. Grant cluster-wide Secret get, with no Secret list, watch, or write permissions.
- [ ] Check wildcards and combined rules too. Operand ServiceAccounts have separate permissions and must remain separate in this check.
- [ ] Initialize the imported state inside `SetupWithManager`, before the two setup functions:

```go
farapi.SetAgentValidator(validation.NewCustomAgentValidator(opts.AgentExists))
farapi.SetOutOfServiceTaintSupported(opts.OutOfServiceTaintSupported)
```

- [ ] Reject nil required options. Return registration errors to `main`, which exits before serving.
- [ ] Register both resources before `mgr.Start`. Do not place registration behind leader election or operand enable logic.
- [ ] Preserve the existing metrics TLS setup and HTTP/2 policy.
- [ ] Configure the webhook server certificate directory explicitly. Preserve certificate reload behavior.
- [ ] Add `mgr.GetWebhookServer().StartedChecker()` to readiness after checking the v0.24.1 API.
- [ ] Keep liveness separate. Do not use FAR Deployment readiness or WAO Service endpoints for the local check.
- [ ] Test missing inventory, parse failure, empty inventory, discovery failure, client failure, and registration failure as startup errors.
- [ ] Test missing certificates and a non-serving listener as not ready. Check both leader and non-leader serving replicas.
- [ ] Serialize tests that replace FAR package globals. Restore a known checker and strategy state after each such test.

```sh
go test ./internal/webhooks/far/... ./cmd/... -count=1
make manifests
go build ./cmd/...
```

**Acceptance:** All startup errors stop WAO. Every serving replica registers the four handlers with an uncached client and checked inventory.

## Task 7: Package inventory generation and keep both FAR images synchronized

**Files:** Modify `config/manager/manager.yaml`, `hack/operand-image-refs.sh`, `hack/sync-operand-images.sh`, and `hack/verify-operand-manifests.sh`. Add tests to `test/offline/image_refs_test.go` and `pipeline_test.go`.

**Consumes:** The collector command and format from Task 3.

**Produces:** Two WAO Pods. Each Pod generates its inventory before the manager starts, even with FAR Disabled.

- [ ] Set `replicas: 2` in the WAO Deployment. Keep `--leader-elect`.
- [ ] Add preferred Pod anti-affinity on `kubernetes.io/hostname`, so the scheduler puts the replicas on different nodes when possible.
- [ ] Check the offline tests and the bundle for assumptions of one replica.

- [ ] Add an init container named `far-agent-inventory` that uses the selected FAR manager image.
- [ ] Override its entrypoint with the collector command from Task 3. Never execute `/manager` from FAR or a fence agent.
- [ ] Add an `emptyDir` named `far-agent-inventory` mounted at `/var/run/wao/far-agent-inventory`.
- [ ] Mount the output read-only in WAO. Mount it writable in the collector.
- [ ] A collector failure must prevent the manager container from starting.
- [ ] Preserve restricted security settings. Check OpenShift arbitrary UIDs, directory permissions, resources, and supported architectures.
- [ ] Do not rebuild the FAR image to add collector tools. Do not add a WAO helper binary.
- [ ] Set the init image from the same `operands.yaml` selection as `RELATED_IMAGE_FAR_MANAGER`.
- [ ] Extend image preflight to require exactly one FAR init container and one FAR manager environment entry.
- [ ] Validate all destinations before modifying any file, including single-operand refresh commands.
- [ ] Extend synchronization to update the init image whenever the FAR image selection changes.
- [ ] Extend verification to reject a mismatched init image. Keep runtime operand placeholders unchanged.
- [ ] Test missing and duplicate init containers, a changed FAR tag, a partial source refresh, and repeat generation.
- [ ] Keep all generation tests offline. They must not pull images or inspect registries.
- [ ] Check installer-related image handling and disconnected substitution. Do not assume an environment variable changes a Pod image field.
- [ ] Document mutable tags. The same reference can resolve to different content before a later operand Pod starts.
- [ ] Do not introduce digest pinning or a public image override as an unapproved solution.

```sh
make operand-manifests
go test ./test/offline -run 'Image|Tag|FAR|Verification' -count=1
make verify-operand-manifests
```

**Acceptance:** Changing the FAR image selection updates both destinations. Every WAO Pod generates a checked inventory before admission starts.

## Task 8: Move registration ownership to classic OLM

**Files:** Modify the source CSV, `config/manifests/kustomization.yaml`, `config/manager/manager.yaml`, and FAR's runtime overlay. Modify `internal/components/far.go`, the reconciler, and their tests. Create `test/offline/far_admission_test.go`.

**Planned controller interface:** Add `FARAdmissionReady func() bool` to `WorkloadAvailabilityConfigReconciler`.
It reports local central admission readiness. A nil function returns false for FAR.
Wire it from the same local listener check used by WAO readiness. Do not add a public API field.

- [ ] Add four `spec.webhookdefinitions` entries to the source CSV.
- [ ] Set each `deploymentName` to the rendered WAO Deployment name, currently `workload-availability-operator-controller-manager`.
- [ ] Derive the final name from rendered manifests in tests. Fail if the definition points at an absent Deployment.
- [ ] Use target container port `9443`. Declare the matching container port in WAO's Deployment.
- [ ] Set `webhookPath`, `generateName`, `type`, `rules`, `failurePolicy`, `sideEffects`, and `admissionReviewVersions` from the admission table.

Example source entry:

```yaml
webhookdefinitions:
  - admissionReviewVersions: [v1]
    containerPort: 9443
    deploymentName: workload-availability-operator-controller-manager
    failurePolicy: Fail
    generateName: vfenceagentsremediation.kb.io
    rules:
      - apiGroups: [fence-agents-remediation.medik8s.io]
        apiVersions: [v1alpha1]
        operations: [CREATE, UPDATE]
        resources: [fenceagentsremediations]
    sideEffects: None
    type: ValidatingAdmissionWebhook
    webhookPath: /validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation
```

- [ ] Repeat the entry with the exact three other rows. Check the pinned OLM API schema before choosing optional fields.
- [ ] Let classic OLM create the central Service, admission configurations, serving Secret, and certificate mounts.
- [ ] Check its actual mount path and key names. Configure WAO for those files without adding cert-manager or service-ca dependencies.
- [ ] Keep native CRD conversion settings unchanged. Do not enable unrelated Kubebuilder certificate scaffolding.
- [ ] Delete the legacy mutating and validating configurations through FAR's runtime overlay.
- [ ] Replace the existing CA annotation patches for those configurations with deletion patches. Check Kustomize patch ordering.
- [ ] Retain FAR's own webhook Service and certificate mount while the unchanged FAR image can run.
- [ ] Remove the two legacy webhook entries from `components.FAR().Webhooks`.
- [ ] Preserve other operands' webhook entries and existing readiness behavior.
- [ ] Report FAR `AdmissionReady` through the injected central callback in enabled and disabled states.
- [ ] Remove the warning that FAR admission is unavailable while Disabled. Preserve warnings for other operands.
- [ ] Keep central objects outside `operands.Render`, reconciliation apply/delete loops, and WAC owner references.
- [ ] Check FAR enable, disable, WAC deletion, and finalization preserve a seeded central registration.
- [ ] Check a foreign legacy configuration remains untouched. Do not add migration or legacy deletion logic.
- [ ] Update the existing all-Enabled controller test that currently expects FAR's legacy mutating configuration.
- [ ] Add offline assertions for exactly four CSV entries, correct paths, operations, Fail policy, and the correct Deployment.
- [ ] Check runtime FAR manifests contain neither admission configuration. Check other operands remain unchanged.
- [ ] Check Secret rules for WAO's ServiceAccount grant only `get`. Check unrelated WAO permissions remain intact.

```sh
make operand-manifests
make manifests
make bundle-reset
go test ./internal/components ./internal/operands -count=1
go test ./test/offline -run FAR -count=1
make test-no-verify-changes
make bundle-validate
```

**Acceptance:** OLM owns central registration. The component controller controls only FAR runtime objects and never removes central admission.

## Task 9: Check the actual handlers and Kubernetes minimum

**Files:** Create `internal/webhooks/far/suite_test.go` and `admission_test.go`. Modify `Makefile` and `.github/workflows/test.yml` to include the new tests.

The existing `test-no-verify-changes` target runs only `./internal/controller/...`.
The new suite must become part of normal local and CI tests.

- [ ] Start envtest with WAO and all operand CRDs. Use the existing copied FAR CRDs.
- [ ] Configure a local TLS webhook server and envtest routing to all four real registered paths.
- [ ] Supply a deterministic checked inventory and the dedicated uncached client.
- [ ] Create synthetic Secrets only in envtest. Register no FAR controllers or CLI executor.
- [ ] Send valid and invalid CREATE requests for FAR and FAR templates through the API server.
- [ ] Send UPDATE requests for both types, including the template-only legacy Secret restriction.
- [ ] Check mutation results and validation errors against Task 5. Direct validator calls alone do not prove routing.

For example, this envtest assertion checks that a registered handler rejects a missing agent:

```go
request := &farapi.FenceAgentsRemediation{
    ObjectMeta: metav1.ObjectMeta{Name: "wao-reject-agent", Namespace: "far-test"},
    Spec: farapi.FenceAgentsRemediationSpec{
        Agent: "fence_wao_missing",
        RemediationStrategy: farapi.ResourceDeletionRemediationStrategy,
        NodeParameters: map[farapi.ParameterName]map[farapi.NodeName]string{
            "--ipport": {"worker-test": "6230"},
        },
    },
}
err := admissionClient.Create(ctx, request, client.DryRunAll)
if err == nil || !strings.Contains(err.Error(), "unsupported fence agent") {
    t.Fatalf("expected unsupported-agent rejection, got %v", err)
}
```

Use this assertion in a standard Go test with the envtest client in scope.
Create the `far-test` namespace during suite setup. The defaulting Secret read must have access to that namespace.

- [ ] Check unsupported agents and invalid strategies through admission. Check malformed objects separately through the CRD schema.
- [ ] Check DELETE does not match the four registration rules.
- [ ] Stop the listener. Check requests fail with Fail policy. Do not change the policy or disable registrations to hide failures.
- [ ] Check startup failure and certificate readiness cases from Task 6.
- [ ] Ensure a second manager can serve handlers without holding the reconciliation leader lease.
- [ ] Add explicit paths to the test target, or a focused `test-webhooks` prerequisite. Avoid silently omitting the new suite.
- [ ] Run controller and webhook suites with default assets and with Kubernetes 1.29 assets.

```sh
make envtest
KUBEBUILDER_ASSETS="$(bin/setup-envtest use '1.36.x!' -p path --bin-dir testbin)" \
  go test ./internal/controller/... ./internal/webhooks/far/... -count=1
KUBEBUILDER_ASSETS="$(bin/setup-envtest use '1.29.x!' -p path --bin-dir testbin)" \
  go test ./internal/controller/... ./internal/webhooks/far/... -count=1
```

- [ ] Make the test suite honor the explicit asset path. Do not allow the existing first-directory fallback to select another version.
- [ ] Add required CI coverage for both `1.29` and `1.36`.
- [ ] Check WAO's existing CEL rejection cases still reject invalid CRs on 1.29.
- [ ] Review generated schemas for newer features unavailable on 1.29. Do not remove CEL or raise the minimum silently.
- [ ] Run focused race tests for inventory reads and non-mutating request handling.

**Acceptance:** CI executes the real handlers and validates CRDs on both versions. Unsupported compatibility is a release blocker, not a documentation claim.

## Task 10: Verify basic classic OLM admission before changing FAR

**Files:** Create the operational sections in `docs/far-webhooks.md`. Store only reviewed, sanitized acceptance results in project documentation.

**Authorization:** Obtain an explicitly selected disposable cluster for the execution session. Earlier cluster use does not authorize this future installation.
Use an explicit kubeconfig in every cluster command. The path below is a usage example, not a selected cluster.

- [ ] Check the cluster has classic OLM and the selected operand images are available.
- [ ] Obtain any required approval for image publication or other external writes before invoking deployment targets.
- [ ] Install through shared medik8s/tools targets. Do not recreate removed cluster scripts.

```sh
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-deploy
```

- [ ] Check every operand remains Disabled. Check no operand Deployment, native config, policy, remediation request, or agent was created.
- [ ] Distinguish the inspection init container from a running FAR manager. Its command must inspect files only.
- [ ] Check all four entries route to WAO. Check their CA bundles, serving certificates, paths, port, and Fail policy.
- [ ] Check no FAR Pod exists while the all-Disabled admission checks run.
- [ ] Use synthetic parameters and server-side dry-run for FAR and FAR template CREATE checks.

Example dry-run input for a valid FAR request:

```yaml
apiVersion: fence-agents-remediation.medik8s.io/v1alpha1
kind: FenceAgentsRemediation
metadata:
  name: wao-admission-check
spec:
  agent: fence_ipmilan
  remediationStrategy: ResourceDeletion
  nodeParameters:
    --ipport:
      worker-test: "6230"
```

Check that `fence_ipmilan` exists in the selected image before using this fixture.
Apply it only with an explicit namespace and `--dry-run=server`. Never apply it as a real remediation request.

- [ ] Check an unsupported agent and invalid action fail. Check template annotation mutation succeeds.
- [ ] Prove no request persisted after dry-run. Use local envtest for UPDATE cases that need pre-existing requests.
- [ ] Use synthetic Secret fixtures only. Never read, copy, or log production Secret values.
- [ ] Check that 2 WAO replicas are ready and that each replica generated its inventory.
- [ ] Restart WAO with FAR Disabled. Check inventory regeneration, serving readiness, and continued admission.
- [ ] Check rolling replacement, a non-leader replica, and certificate renewal without request failures beyond the documented startup interval.
- [ ] Use supported installer controls for renewal. Do not delete arbitrary certificate Secrets to simulate renewal.
- [ ] Delete the WAC singleton in the disposable installation. Check central registrations remain through deletion and recreation.
- [ ] Record basic all-Disabled admission success before starting Task 11 in FAR.

If checking FAR Enabled before Task 11, keep its existing certificate Service and mounts.
On OpenShift, check service-ca supplies the required certificate. The unchanged FAR listener still requires certificate files.
Central WAO certificates still come from classic OLM. Do not redirect admission to the FAR listener.

- [ ] Enable and then disable FAR without creating native remediation objects. Check central admission remains available in both states.
- [ ] Uninstall with the shared target. Check only WAO-owned central registrations are removed. Check foreign objects remain unchanged.

```sh
KUBECONFIG=/path/to/disposable.kubeconfig make dev-olm-undeploy
make bundle-reset
```

- [ ] Check removal through the API. Do not infer removal from command exit status alone.
- [ ] Record sanitized versions, test outcomes, certificate provider, and remaining limits. Exclude cluster names, credentials, and Secret data.

**Acceptance:** Classic OLM supplies certificates. All four WAO admission entries work with FAR Disabled. Lifecycle and uninstall preserve ownership rules.

## Task 11: Add the deferred FAR flag, then remove unused FAR certificate resources

**External repository files:** `../fence-agents-remediation/cmd/main.go` and focused startup tests in that repository.

**WAO files:** `operands.yaml`, `config/operands/far/overlays/runtime/`, generated runtime manifests, generated bundle, and `docs/far-webhooks.md`.

**Precondition:** Task 10 has demonstrated basic FAR admission in WAO. Do not make this task a prerequisite for that proof.

- [ ] Read FAR's instructions before changing its source. Keep the patch limited to the agreed startup flag.
- [ ] Add `--enable-webhooks` with default `true`.
- [ ] Guard both webhook setup calls. Leave both controllers active in either mode.

```go
flag.BoolVar(&enableWebhooks, "enable-webhooks", true, "Enable admission webhooks")
```

Place the existing two setup calls inside `if enableWebhooks`. Retain their existing error handling.
Keep existing server option construction, capability discovery, metrics TLS, and standalone manifests unless tests prove a required narrow change.

- [ ] Check the omitted flag and explicit true preserve standalone admission.
- [ ] Check explicit false starts without webhook certificates and opens no webhook listener.
- [ ] Check false still runs both controllers. Check metrics TLS remains unchanged.
- [ ] If server construction starts a listener without registrations, correct that narrow behavior and test it explicitly.
- [ ] Review the FAR change and its tests before any publication. Obtain approval for each GitHub write or push.
- [ ] Select an available FAR image that is known to support the flag. Do not invent a release tag.
- [ ] Change WAO's FAR image selection and `--enable-webhooks=false` together.
- [ ] Refresh copied FAR manifests only through `make update-far-manifests` if source configuration changed.
- [ ] Remove FAR's now-unused webhook Service, certificate volume, and mount through its runtime overlay.
- [ ] Preserve metrics certificates and unrelated Services. Do not share WAO's serving private key with FAR.
- [ ] Keep central admission definitions unchanged. Keep the inventory init container on the same selected FAR image.
- [ ] Check rendered FAR Deployment arguments, absent webhook mounts, and absent legacy registrations in offline tests.

```sh
make operand-manifests
make bundle-reset
make check
make test-offline
make test-no-verify-changes
```

- [ ] On a newly authorized disposable installation, check FAR enable/disable, both controller processes, and central dry-run admission again.

**Acceptance:** FAR can run without its unused webhook listener or certificates. WAO remains the only FAR admission backend.

## Task 12: Verify the desired OLM v1 path and finish contributor documentation

**Files:** Complete `docs/far-webhooks.md`. Inspect existing `hack/olmv1-make-and-deploy-wao.sh`, `hack/olmv1-purge-wao.sh`, and `config/catalog/olmv1/` before proposing changes.

Classic OLM remains the release requirement. Optional OLM v1 verification must not delay the required v0 result.
Use the same `registry+v1` bundle and CSV definitions where the selected OLM v1 distribution supports them.

The source investigation checked operator-controller `v1.12.0`. It can generate webhook Services, registrations, and mounts from CSV definitions.
Its mount interface is `/tmp/k8s-webhook-server/serving-certs` with keys `tls.crt` and `tls.key`.
This is source evidence, not verified support for the target cluster or the full WAO bundle.

- [ ] Select the actual OLM v1 distribution, version, and certificate provider before claiming support.
- [ ] Check versioned provider documentation and installed controller settings.
- [ ] For cert-manager, check generated Issuer and Certificate objects and CA injection.
- [ ] For OpenShift service-ca, check serving-cert and CA-injection annotations.
- [ ] Check feature gates on operator-controller. In the checked source, both providers default true and cert-manager takes priority.
- [ ] To select service-ca in that source, use `WebhookProviderCertManager=false` and `WebhookProviderOpenshiftServiceCA=true`.
- [ ] Treat those as installer settings, not WAO or FAR arguments. Do not change shared installer settings without authorization.
- [ ] Check serving files use WAO's configured interface. Keep provider logic outside FAR validation and inventory code.
- [ ] Check installation permissions for generated Services, Secrets, admission configurations, and provider resources.
- [ ] Check all-Disabled inventory, all four dry-run handlers, certificate renewal, enable/disable, and uninstall separately under OLM v1.
- [ ] Check broader bundle installation too. Working webhook rendering alone does not prove full OLM v1 support.
- [ ] If the target provider or version cannot support the bundle, document the exact limit. Do not claim verified OLM v1 support.
- [ ] Complete contributor documentation for dependency updates, copied-source review, inventory generation, Secret access, readiness, and lifecycle.
- [ ] Include the manual transition procedure and its upgrade exclusions.
- [ ] Link the existing Secret logging issue and any separately approved follow-up issues.

Versioned source references:

- [Webhook support guide](https://github.com/operator-framework/operator-controller/blob/v1.12.0/docs/draft/howto/enable-webhook-support.md)
- [Controller feature settings](https://github.com/operator-framework/operator-controller/blob/v1.12.0/cmd/operator-controller/main.go)
- [Registry v1 generation](https://github.com/operator-framework/operator-controller/blob/v1.12.0/internal/operator-controller/rukpak/render/registryv1/generators/generators.go)
- [Certificate providers](https://github.com/operator-framework/operator-controller/tree/v1.12.0/internal/operator-controller/rukpak/render/certproviders)

**Acceptance:** Required v0 support is checked. OLM v1 has either independent test evidence or a precise documented limit.

## Final review and handoff checks

- [ ] Check the dependency update remained an independent change.
- [ ] Check the inventory meets Task 3. Check `docs/far-webhooks.md` contains the inventory differences and the Secret logging concern.
- [ ] Check the FAR source pin, copy licenses, and adaptations match the recorded source.
- [ ] Check all four paths, rules, Fail policies, and certificate interfaces match the implementation.
- [ ] Check the new webhook suite runs in CI on Kubernetes 1.29 and 1.36.
- [ ] Check all-Disabled behavior, each serving replica, WAC deletion, FAR disable, and WAO uninstall.
- [ ] Check basic WAO admission evidence predates the FAR disabling flag change.
- [ ] Check generated runtime objects and the normalized bundle match their source configuration.
- [ ] Run `make check` before each local commit. Use small commits with the required attribution trailer.
- [ ] Run `make test-offline` and `make test` from a clean committed checkout before an approved push.
- [ ] Review the final diff for unrelated changes, credentials, cluster identifiers, and Secret-derived data.
- [ ] Report commands and outcomes. Separate checked behavior, optional checks not run, and remaining blockers.

No implementation, commit, GitHub write, or cluster action forms part of creating this plan.
