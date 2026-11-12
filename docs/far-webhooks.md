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
