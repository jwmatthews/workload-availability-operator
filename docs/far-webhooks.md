# FAR admission webhooks in WAO

The WAO manager serves the admission webhooks of Fence Agents Remediation (FAR).
The webhooks operate when the FAR operand is `Disabled` and when it is `Enabled`.
[SOURCE.md](../internal/webhooks/far/SOURCE.md) records the FAR source pin and the copied files.

## Agent inventory

FAR admission accepts a fence agent only if the agent is in the selected FAR image.
WAO does not contain the fence agents. Thus each WAO Pod collects a name list from the FAR image before the manager starts.

### Format

- The file is `/var/run/wao/far-agent-inventory/agents.txt`.
- The file is UTF-8 text with one agent name on each line. Each line ends with a newline character.
- Each name must match `^fence_[^/\s]+$` and contain no more than 255 bytes.
- The file must contain between 1 and 1024 names, no duplicate names, and no empty lines.
- The file contains no image reference and no architecture.

`internal/webhooks/far/inventory.Load` reads the file one time at manager start.
The manager stops if the file is missing or invalid.
The list is a snapshot from the start of the WAO Pod. WAO does not read the file again.

### Known differences from the FAR checker

The FAR checker calls `os.Stat(filepath.Join("/usr/sbin/", agent))`.
WAO accepts an agent only if its exact name is in the list. These differences are approved.

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

WAO does not check that an agent is executable or that it is a regular file.

## Secret access and the Secret logging concern

FAR parameters can come from Secrets. Admission must read those Secrets to check the parameters.
Thus the WAO manager has cluster-wide `get` permission on Secrets. It has no `list`, `watch`, or write permission on Secrets.
WAO reads each Secret directly from the API server with a dedicated client. WAO does not keep Secrets in a cache.

### Concern: an action value from a Secret can go into the manager log

The FAR function `validateFenceAction` in `api/v1alpha1/fenceagentsremediation_params.go` contains this log statement:

```go
paramsLog.Error(err, "can't build CR with this action attribute", "action", paramVal)
```

- `paramVal` is the value of the `action` or `--action` parameter. This value can come from a Secret.
- The statement operates only when the value is not empty, not `reboot`, and not `off`.
- Standalone FAR writes this value to the FAR manager log. WAO imports the same code, thus the value goes into the WAO manager log.

A fix is outside the scope of the FAR webhook work. WAO adds no logging adapter and makes no FAR logging change.
[Issue 2](https://github.com/jwmatthews/workload-availability-operator/issues/2) is the tracker for a later fix.

### Rules for tests

Use only synthetic Secret values in tests and on clusters.
Do not read, copy, or log production Secret values.
