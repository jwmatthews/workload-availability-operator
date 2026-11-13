# NHC webhook source record

WAO serves the Node Healthcheck Operator (NHC) admission webhook. WAO imports the public NHC API package.
WAO copies only the files that Go does not let another module import (`internal/`).

## Source pin

| Item | Value |
| --- | --- |
| Source repository | <https://github.com/medik8s/node-healthcheck-operator> |
| Branch | `wao` |
| Revision | `8b326d3acfe5cc44029057486a35e9ea594d25cb` |
| Go module | `github.com/medik8s/node-healthcheck-operator/v5` |
| Resolved module version | `v5.0.0-20261103000740-8b326d3acfe5` |
| License | Apache License 2.0. The copied files keep their license headers. |

The image tag in `operands.yaml` does not select this revision. Update this record when `go.mod` selects a different NHC version.

## Imported packages (not copied)

- `github.com/medik8s/node-healthcheck-operator/v5/api/v1alpha1`: API types and `ValidateMinHealthyMaxUnhealthy`.

## Copied files

| Source file | WAO file | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook.go` | `v1alpha1/nodehealthcheck_webhook.go` | The import paths of `cluster` and `annotations` point at the WAO copies. `SetupWebhookWithManager` receives `admissionClient client.Client`. The validator uses that client instead of `mgr.GetClient()`. |
| `internal/webhook/v1alpha1/nodehealthcheck_webhook_test.go` | `v1alpha1/nodehealthcheck_webhook_test.go` | None. |
| `internal/webhook/v1alpha1/webhook_suite_test.go` | `v1alpha1/webhook_suite_test.go` | The import path of `cluster`. The CRD and webhook paths point to `config/operands/nhc/config/`. A missing CRD path is an error. The setup call passes the test client. |
| `internal/controller/cluster/capabilities.go` | `cluster/capabilities.go` | None. |
| `internal/controller/utils/annotations/annotations.go` | `annotations/annotations.go` | None. |

The copied files can have a different import order. The NHC module is an external module in WAO, and `make test-imports` puts it in the external group.
The validation bodies and the `+kubebuilder:webhook` marker are unchanged.
`make manifests` reads the marker and writes the entry `vnodehealthcheck.kb.io` into `config/webhook/manifests.yaml`.
WAO does not copy NHC controllers, `cluster/upgrade_checker.go`, the MachineHealthCheck code, or the feature gate code.

## How to review a refresh

Run this command from the WAO repository root with an NHC checkout at the pinned revision:

```sh
nhc=../node-healthcheck-operator
for f in internal/webhooks/nhc/v1alpha1/*.go; do
  [ -f "$nhc/internal/webhook/v1alpha1/$(basename "$f")" ] && diff -u "$nhc/internal/webhook/v1alpha1/$(basename "$f")" "$f"
done
diff -u "$nhc/internal/controller/cluster/capabilities.go" internal/webhooks/nhc/cluster/capabilities.go
diff -u "$nhc/internal/controller/utils/annotations/annotations.go" internal/webhooks/nhc/annotations/annotations.go
```

The output must show only the adaptations in the table and the import order. Files that WAO adds (not copied) are listed below.

## WAO files in the copied package

- `v1alpha1/wao_behavior_test.go`: tests for the validation behavior that WAO must preserve.
