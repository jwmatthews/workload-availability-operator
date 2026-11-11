# WAO test punchlist

Track issues found while running the existing medik8s system tests against the
Workload Availability Operator (WAO). WAO installs the medik8s operators as
components of one operator.

Add new items with sequential IDs. Keep completed items and record how each fix
was checked. Separate reported observations from confirmed causes.

## Open items

- [ ] **WAO-001: Prevent the log watcher from selecting another component's pod.**

### WAO-001: Log watcher can select another component's pod

- **Status:** Open. Reported during a test run. Cause and affected code need investigation.
- **Reported:** 2026-11-05.
- **Test:** Machine Deletion Remediation (MDR), using the existing medik8s system tests against WAO.
- **Area:** Deployment selectors and the log watcher.

**Reported observation**

The monitoring session reported that several deployments share the same selector.
The watcher's deployment log command can therefore select another operator's pod.
The session read the MDR pod logs directly to continue monitoring.

**Test context**

The monitoring session also reported that the Machine API controller had moved to
a healthy node and was running. This was a recovery observation, not an additional defect.

**Effect**

The watcher can show another component's logs while monitoring MDR.
This can cause incorrect conclusions about the MDR controller's state.

**Temporary procedure**

Read logs from the MDR pod directly. Check that the pod belongs to MDR before reading its logs.

**Work to complete**

- [ ] Identify the watcher code and its exact deployment log command.
- [ ] Check which WAO component deployments have overlapping selectors.
- [ ] Determine whether WAO packaging, the watcher, or both require changes.
- [ ] Make log selection specific to the intended component.
- [ ] Check that the watcher reads MDR logs when multiple WAO components run together.
- [ ] Record the changed files and the check results before closing this item.

**Completion criteria**

The watcher selects only the intended component's pods when multiple WAO components
run together. Record any selector changes and check that deployments and services
still select their intended pods.
