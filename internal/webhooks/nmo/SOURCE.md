# NMO webhook source record

WAO serves the Node Maintenance Operator (NMO) admission webhook. WAO imports the public NMO packages.
WAO copies only the files that Go does not let another module import (`internal/`).

## Source pin

| Item | Value |
| --- | --- |
| Source repository | <https://github.com/medik8s/node-maintenance-operator> |
| Branch | `wao` |
| Revision | `5baa709167a2325aa61be37417fc692a69cf27e6` |
| Go module | `github.com/medik8s/node-maintenance-operator/v5` |
| Resolved module version | `v5.0.0-20261013171824-5baa709167a2` |
| License | Apache License 2.0. The copied files keep their license headers. |

The image tag in `operands.yaml` does not select this revision. Update this record when `go.mod` selects a different NMO version.

## Imported packages (not copied)

- `github.com/medik8s/node-maintenance-operator/v5/api/v1beta1`: API types.
- `github.com/medik8s/node-maintenance-operator/v5/pkg/utils`: `NewOpenshiftValidator`, the OpenShift check of the NMO manager.
- `github.com/medik8s/common/pkg/etcd` and `pkg/nodes`: the etcd quorum check and the control plane check. WAO and NMO require the same `medik8s/common` version.

## Copied files

| Source file | WAO file | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1beta1/nodemaintenance_webhook.go` | `v1beta1/nodemaintenance_webhook.go` | `SetupNodeMaintenanceWebhookWithManager` receives `admissionClient client.Client`. The validator uses that client instead of `mgr.GetClient()`. |
| `internal/webhook/v1beta1/nodemaintenance_webhook_test.go` | `v1beta1/nodemaintenance_webhook_test.go` | None. |
| `internal/webhook/v1beta1/webhook_suite_test.go` | `v1beta1/webhook_suite_test.go` | The CRD and webhook paths point to `config/operands/nmo/config/`. A missing CRD path is an error. The setup call passes the test client. |

The validation bodies and the `+kubebuilder:webhook` marker are unchanged.
`make manifests` reads the marker and writes the entry `vnodemaintenance.kb.io` into `config/webhook/manifests.yaml`.
WAO does not copy the NMO controller, the lease code or the drain code.

Difference from NMO at run time: NMO gives the validator the manager client, which reads from a cache. WAO gives it a client that reads from the API server.

## How to review a refresh

Run this command from the WAO repository root with an NMO checkout at the pinned revision:

```sh
nmo=../node-maintenance-operator
for f in internal/webhooks/nmo/v1beta1/*.go; do
  [ -f "$nmo/internal/webhook/v1beta1/$(basename "$f")" ] && diff -u "$nmo/internal/webhook/v1beta1/$(basename "$f")" "$f"
done
```

The output must show only the adaptations in the table and the import order. Files that WAO adds (not copied) are listed below.

## WAO files in the copied package

- `v1beta1/wao_behavior_test.go`: tests for the validation behavior that WAO must preserve.
