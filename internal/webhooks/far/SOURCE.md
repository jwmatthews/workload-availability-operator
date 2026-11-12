# FAR webhook source record

WAO serves the Fence Agents Remediation (FAR) admission webhooks. WAO imports the public FAR packages.
WAO copies only the files that Go does not let another module import (`internal/`).

## Source pin

| Item | Value |
| --- | --- |
| Source repository | <https://github.com/medik8s/fence-agents-remediation> |
| Branch | `wao` |
| Revision | `58e8107d32e51ca28f285c786ed7a9c18b193a25` |
| Go module | `github.com/medik8s/fence-agents-remediation/v5` |
| Resolved module version | `v5.0.0-20261103000740-58e8107d32e5` |
| License | Apache License 2.0. The copied files keep their license headers. |

The image tag in `operands.yaml` does not select this revision. Update this record when `go.mod` selects a different FAR version.

## Imported packages (not copied)

- `github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1`: API types, `FARValidator`, `FARTemplateValidator`, parameter checks.
- `github.com/medik8s/fence-agents-remediation/v5/pkg/validation`: agent validator and out-of-service taint discovery.

## Copied files

All copied files come from `internal/webhook/v1alpha1/` of the source revision. They are in `internal/webhooks/far/v1alpha1/`.

| File | Adaptation |
| --- | --- |
| `fenceagentsremediation_webhook.go` | `SetupFARWebhookWithManager` receives `admissionClient client.Client`. The validator and the defaulter use that client instead of `mgr.GetClient()`. |
| `fenceagentsremediationtemplate_webhook.go` | `SetupFARTemplateWebhookWithManager` receives `admissionClient client.Client`, with the same use. |
| `fenceagentsremediation_webhook_test.go` | None. |
| `fenceagentsremediationtemplate_webhook_test.go` | None. |
| `webhook_suite_test.go` | The CRD and webhook paths point to `config/operands/far/config/`. A missing CRD path is an error. The setup calls pass the test client. |

All five files have a different import order. The FAR module is an external module in WAO, and `make test-imports` puts it in the external group.
The defaulting bodies and the `+kubebuilder:webhook` markers are unchanged.
`make manifests` reads the markers and writes `config/webhook/manifests.yaml`. No kustomization uses that file. Tests use it as the record of the four paths and rules.
WAO does not copy FAR controllers, parameter validators, template libraries, or the CLI executor.

## How to review a refresh

Run this command from the WAO repository root with a FAR checkout at the pinned revision:

```sh
for f in internal/webhooks/far/v1alpha1/*.go; do
  diff -u "../fence-agents-remediation/internal/webhook/v1alpha1/$(basename "$f")" "$f"
done
```

The output must show only the adaptations in the table and the import order. Files that WAO adds (not copied) are listed below.

## WAO files in the copied package

None.
