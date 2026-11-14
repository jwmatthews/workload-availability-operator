# SNR webhook source record

WAO serves the Self Node Remediation (SNR) admission webhooks. WAO imports the public SNR packages.
WAO copies only the files that Go does not let another module import (`internal/`).

## Source pin

| Item | Value |
| --- | --- |
| Source repository | <https://github.com/medik8s/self-node-remediation> |
| Branch | `wao` |
| Revision | `96a9372cda44c0062fe1d2b28d79685557ab6502` |
| Go module | `github.com/medik8s/self-node-remediation/v5` |
| Resolved module version | `v5.0.0-20261102205523-96a9372cda44` |
| License | Apache License 2.0. The copied files keep their license headers. |

The image tag in `operands.yaml` does not select this revision. Update this record when `go.mod` selects a different SNR version.

## Imported packages (not copied)

- `github.com/medik8s/self-node-remediation/v5/api/v1alpha1`: API types and `ConfigCRName`.
- `github.com/medik8s/common/pkg/annotations`: the annotation name of the template defaulter. WAO and SNR require the same `medik8s/common` version.

## Copied files

| Source file | WAO file | Adaptation |
| --- | --- | --- |
| `internal/webhook/v1alpha1/selfnoderemediationconfig_webhook.go` | `v1alpha1/selfnoderemediationconfig_webhook.go` | Import path of `utils`. |
| `internal/webhook/v1alpha1/selfnoderemediationtemplate_webhook.go` | `v1alpha1/selfnoderemediationtemplate_webhook.go` | Import path of `utils`. |
| `internal/webhook/v1alpha1/selfnoderemediation_webhook.go` | `v1alpha1/selfnoderemediation_webhook.go` | None. |
| `internal/webhook/v1alpha1/selfnoderemediationconfig_webhook_test.go` | `v1alpha1/selfnoderemediationconfig_webhook_test.go` | The test sets `OPERATOR_NAMESPACE`, not `DEPLOYMENT_NAMESPACE`. |
| `internal/webhook/v1alpha1/selfnoderemediationtemplate_webhook_test.go` | `v1alpha1/selfnoderemediationtemplate_webhook_test.go` | Import path of `utils`. |
| `internal/webhook/v1alpha1/selfnoderemediation_webhook_test.go` | `v1alpha1/selfnoderemediation_webhook_test.go` | Import path of `utils`. |
| `internal/webhook/v1alpha1/webhook_suite_test.go` | `v1alpha1/webhook_suite_test.go` | The CRD and webhook paths point to `config/operands/snr/config/`. A missing CRD path is an error. |
| `internal/utils/namespaces.go` | `utils/namespaces.go` | `GetDeploymentNamespace` reads `OPERATOR_NAMESPACE`, not `DEPLOYMENT_NAMESPACE` (decision S2). |
| `internal/utils/taints.go` | `utils/taints.go` | None. |

The validation bodies, the defaulter and the four `+kubebuilder:webhook` markers are unchanged.
`make manifests` reads the markers and writes four entries into `config/webhook/manifests.yaml`.
The entry `vselfnoderemediation.kb.io` has `failurePolicy=ignore` in its marker. Do not change it (decision S1).
WAO does not copy the SNR controllers, the agent code or the other files of `internal/utils`.
WAO does not call `TaintExists`, `DeleteTaint` and `IsOutOfServiceTaintGA` of `taints.go`. The file is a complete copy (decision S4).

Differences from SNR at run time:

- SNR reads the namespace from `DEPLOYMENT_NAMESPACE`. WAO reads it from `OPERATOR_NAMESPACE`. WAO installs each operand in its own namespace, so the value is the same.
- SNR continues when the read of the Kubernetes version fails at start. WAO stops (decision S3). See `setup.go`.

## How to review a refresh

Run this command from the WAO repository root with an SNR checkout at the pinned revision:

```sh
snr=../self-node-remediation
for f in internal/webhooks/snr/v1alpha1/*.go; do
  [ -f "$snr/internal/webhook/v1alpha1/$(basename "$f")" ] && diff -u "$snr/internal/webhook/v1alpha1/$(basename "$f")" "$f"
done
for f in internal/webhooks/snr/utils/*.go; do
  diff -u "$snr/internal/utils/$(basename "$f")" "$f"
done
```

The output must show only the adaptations in the table and the import order. Files that WAO adds (not copied) are listed below.

## WAO files in the copied package

- `v1alpha1/wao_behavior_test.go`: tests for the defaulting and validation behavior that WAO must preserve.
