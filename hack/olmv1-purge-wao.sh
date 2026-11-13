#!/usr/bin/env bash
# Deep clean of an OLM v1 install of the Workload Availability Operator (WAO) POC from a
# disposable cluster. The OLM v0 equivalent is hack/wao-purge.sh.
#
# Removes what WAO, its six operands (NHC, SNR, SBR, FAR, MDR, NMO) and OLM v1 create, in an
# order that lets controllers run their own finalizers before they are removed:
#
#   1. Operand custom resources, while the operand managers are still running
#   2. The WorkloadAvailabilityConfig (its finalizer removes operand Deployments and webhooks)
#   3. The ClusterExtension (OLM v1 uninstalls the bundle: WAO Deployment, RBAC, CRDs) and
#      the ClusterCatalog
#   4. Leftover webhooks, stuck finalizers, then any CRDs that are still present
#   5. Cluster-scoped leftovers (RBAC, ConsolePlugin), then the namespace(s)
#   6. A report of anything that could not be safely removed automatically
#
# Every step is idempotent: re-running after a partial failure is safe.
#
# Usage, from anywhere:
#   ./hack/wao-olmv1-purge.sh            # asks before deleting
#   ASSUME_YES=1 ./hack/wao-olmv1-purge.sh   # no prompts; keeps the shared medik8s-leases namespace

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
export KUBECONFIG="${KUBECONFIG:-$PWD/jmatthews-rhwa-ocp5-rc6.kubeconfig}"
ASSUME_YES="${ASSUME_YES:-0}"

NS=openshift-workload-availability
PKG=workload-availability-operator
EXTENSION=workload-availability-operator
CATALOG=wao-poc-catalog
# Namespace the medik8s operators (NHC, NMO, SNR) create for node leases.
LEASE_NS=medik8s-leases
# Label WAO puts on every operand object it renders (internal/operands/render.go).
WAO_MANAGED_LABEL="app.kubernetes.io/managed-by=${PKG}"
# Labels OLM v1 puts on every object it installs for the ClusterExtension.
OLMV1_OWNER_LABEL="olm.operatorframework.io/owner-kind=ClusterExtension,olm.operatorframework.io/owner-name=${EXTENSION}"
# Name prefixes of everything the bundle ships; used only for the final report.
NAME_PREFIXES='^(workload-availability|node-healthcheck|node-remediation|self-node-remediation|sbr-operator|storage-based-remediation|fence-agents-remediation|machine-deletion-remediation|node-maintenance)'

# Operand CRDs, in the order their instances are deleted: stop NHC first so it creates no new
# remediations, end NodeMaintenances (uncordons nodes), end remediations, then templates and
# configs (configs own the SNR/SBR agent DaemonSets).
OPERAND_CRDS=(
  nodehealthchecks.remediation.medik8s.io
  nodemaintenances.nodemaintenance.medik8s.io
  selfnoderemediations.self-node-remediation.medik8s.io
  storagebasedremediations.storage-based-remediation.medik8s.io
  fenceagentsremediations.fence-agents-remediation.medik8s.io
  machinedeletionremediations.machine-deletion-remediation.medik8s.io
  selfnoderemediationtemplates.self-node-remediation.medik8s.io
  storagebasedremediationtemplates.storage-based-remediation.medik8s.io
  fenceagentsremediationtemplates.fence-agents-remediation.medik8s.io
  machinedeletionremediationtemplates.machine-deletion-remediation.medik8s.io
  selfnoderemediationconfigs.self-node-remediation.medik8s.io
  storagebasedremediationconfigs.storage-based-remediation.medik8s.io
)
WAO_CRD=workloadavailabilityconfigs.workloadavailability.medik8s.io

# ---------------------------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------------------------

# ask PROMPT [DEFAULT_WHEN_ASSUME_YES]
ask() {
  local reply
  if [ "$ASSUME_YES" = "1" ]; then
    echo "${1:-Continue?} [auto: ${2:-y}]"
    [ "${2:-y}" = "y" ]
    return
  fi
  read -r -p "${1:-Continue?} [y/N] " reply || reply=""
  [[ "$reply" == [Yy] ]]
}

confirm() {
  ask "$@" || { echo "Aborted." >&2; exit 1; }
}

step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }

crd_exists() { oc get crd "$1" >/dev/null 2>&1; }

# Remove finalizers from every instance of a resource type, in all namespaces. Used only after
# the owning controller is gone, when nothing is left to process them.
strip_finalizers() {
  local resource=$1 ns name
  oc get "$resource" -A --ignore-not-found \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"\n"}{end}' 2>/dev/null |
    while IFS='|' read -r ns name; do
      [ -n "$name" ] || continue
      echo "  removing finalizers from $resource ${ns:+$ns/}$name"
      oc patch "$resource" "$name" ${ns:+-n "$ns"} --type merge \
        -p '{"metadata":{"finalizers":null}}' >/dev/null || true
    done || true
}

# Remove the remediation status from every NodeHealthCheck. The NHC webhook, which the WAO manager
# serves, rejects the delete of a NodeHealthCheck whose status shows a remediation. No controller
# removes that status while NHC is Disabled. The webhook does not validate the status subresource.
# See docs/nhc-webhooks.md.
clear_nhc_remediation_status() {
  local name
  oc get nodehealthchecks.remediation.medik8s.io --ignore-not-found \
    -o jsonpath='{range .items[?(@.status.unhealthyNodes)]}{.metadata.name}{"\n"}{end}' 2>/dev/null |
    while read -r name; do
      [ -n "$name" ] || continue
      echo "  removing status.unhealthyNodes from NodeHealthCheck $name"
      oc patch nodehealthchecks.remediation.medik8s.io "$name" --subresource=status --type merge \
        -p '{"status":{"unhealthyNodes":null}}' >/dev/null || true
    done || true
}

# ---------------------------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------------------------

if ! oc whoami; then
  echo "ERROR: 'oc whoami' failed with KUBECONFIG=${KUBECONFIG:-<unset>}" >&2
  exit 1
fi
echo "Using cluster: $(oc whoami --show-server)"
echo "This removes WAO, all six operands, their CRDs and ALL their custom resources cluster-wide."
confirm "Continue?"

# ---------------------------------------------------------------------------------------------
# 1. Operand custom resources, while the managers can still run their finalizers
# ---------------------------------------------------------------------------------------------

step "1. Deleting operand custom resources in all namespaces"
for crd in "${OPERAND_CRDS[@]}"; do
  crd_exists "$crd" || continue
  echo "  $crd"
  [ "$crd" != nodehealthchecks.remediation.medik8s.io ] || clear_nhc_remediation_status
  oc delete "$crd" --all -A --ignore-not-found --timeout=120s ||
    warn "could not delete all $crd (finalizers are cleared in step 4)"
done

# ---------------------------------------------------------------------------------------------
# 2. WorkloadAvailabilityConfig: its finalizer deletes the operand Deployments, Services,
#    ConfigMaps and webhook configurations WAO rendered
# ---------------------------------------------------------------------------------------------

step "2. Deleting WorkloadAvailabilityConfig"
if crd_exists "$WAO_CRD"; then
  oc delete "$WAO_CRD" --all --ignore-not-found --timeout=180s ||
    warn "WorkloadAvailabilityConfig did not finalize (is the WAO pod running?); cleared in step 4"
fi

# ---------------------------------------------------------------------------------------------
# 3. OLM v1 objects. Deleting the ClusterExtension makes OLM v1 remove everything it installed
#    from the bundle: the WAO Deployment, ServiceAccounts, RBAC and all 13 CRDs.
# ---------------------------------------------------------------------------------------------

step "3. Removing OLM v1 objects"
oc delete clusterextension "$EXTENSION" --ignore-not-found --timeout=300s ||
  warn "ClusterExtension $EXTENSION did not finish uninstalling; leftovers are removed below"
oc delete clustercatalog "$CATALOG" --ignore-not-found --timeout=120s ||
  warn "ClusterCatalog $CATALOG did not delete"

# ---------------------------------------------------------------------------------------------
# 4. Webhooks, stuck finalizers and CRDs. Webhooks go first: once their backing Service is gone,
#    they would reject the finalizer patches and CR deletions below.
# ---------------------------------------------------------------------------------------------

step "4a. Deleting operand webhook configurations"
oc delete validatingwebhookconfiguration,mutatingwebhookconfiguration \
  -l "$WAO_MANAGED_LABEL" --ignore-not-found
oc delete validatingwebhookconfiguration,mutatingwebhookconfiguration \
  -l "$OLMV1_OWNER_LABEL" --ignore-not-found

step "4b. Clearing finalizers left with no controller to process them"
for crd in "${OPERAND_CRDS[@]}" "$WAO_CRD"; do
  crd_exists "$crd" && strip_finalizers "$crd"
done

step "4c. Deleting CRDs still present (normally already removed with the ClusterExtension)"
for crd in "${OPERAND_CRDS[@]}" "$WAO_CRD"; do
  oc delete crd "$crd" --ignore-not-found --timeout=120s || warn "CRD $crd did not delete"
done

# ---------------------------------------------------------------------------------------------
# 5. Cluster-scoped leftovers and namespaces
# ---------------------------------------------------------------------------------------------

step "5a. Deleting cluster RBAC owned by this install"
# Installed by OLM v1 from the bundle (normally already removed with the ClusterExtension).
oc delete clusterrole,clusterrolebinding -l "$OLMV1_OWNER_LABEL" --ignore-not-found
# Created at runtime by operands, e.g. NHC's aggregation role (its owner reference points at a
# namespaced Deployment, so garbage collection never removes it).
oc delete clusterrole node-healthcheck-operator-aggregation --ignore-not-found
# Created at runtime (for example by the SBR manager): any binding whose subject lives in $NS.
for crb in $(oc get clusterrolebinding \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.subjects[*].namespace}{"\n"}{end}' |
  awk -v ns="$NS" '{for (i = 2; i <= NF; i++) if ($i == ns) {print $1; break}}'); do
  oc delete clusterrolebinding "$crb" --ignore-not-found
done

step "5b. Removing console plugins served from ${NS}"
for plugin in $(oc get consoleplugin \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.backend.service.namespace}{"\n"}{end}' 2>/dev/null |
  awk -v ns="$NS" '$2 == ns {print $1}'); do
  # Disable it in the console operator first: remove its entry from spec.plugins.
  i=0
  for enabled in $(oc get consoles.operator.openshift.io cluster -o jsonpath='{.spec.plugins[*]}'); do
    if [ "$enabled" = "$plugin" ]; then
      oc patch consoles.operator.openshift.io cluster --type json \
        -p "[{\"op\":\"remove\",\"path\":\"/spec/plugins/$i\"}]"
      break
    fi
    i=$((i + 1))
  done
  oc delete consoleplugin "$plugin" --ignore-not-found
done

step "5c. Deleting namespace ${NS} (secrets, PVCs, leases, DaemonSets and anything else left in it)"
oc delete namespace "$NS" --ignore-not-found --timeout=300s ||
  warn "namespace $NS is still terminating; inspect: oc get ns $NS -o jsonpath='{.status.conditions}'"

if oc get namespace "$LEASE_NS" >/dev/null 2>&1; then
  step "5d. Lease namespace ${LEASE_NS}"
  echo "  Shared by all medik8s operators; delete it only if no other medik8s operator is installed."
  oc get lease -n "$LEASE_NS" 2>/dev/null || true
  if ask "Delete namespace ${LEASE_NS}?" n; then
    oc delete namespace "$LEASE_NS" --ignore-not-found --timeout=120s || warn "$LEASE_NS did not delete"
  fi
fi

# ---------------------------------------------------------------------------------------------
# 6. Report what could not be removed safely by this script
# ---------------------------------------------------------------------------------------------

step "6. Checking for leftovers (empty sections mean clean)"

echo "-- OLM v1 objects:"
oc get clusterextension "$EXTENSION" --ignore-not-found -o name
oc get clustercatalog "$CATALOG" --ignore-not-found -o name

echo "-- CRDs:"
oc get crd -o name | grep -E 'medik8s\.io$' || true

echo "-- Cluster-scoped objects with WAO or operand names (could belong to a separate medik8s install):"
oc get clusterrole,clusterrolebinding,validatingwebhookconfiguration,mutatingwebhookconfiguration,consoleplugin \
  -o name 2>/dev/null | awk -F/ -v re="$NAME_PREFIXES" '$2 ~ re' || true

echo "-- SecurityContextConstraints granting users in ${NS} (SBR patches SCCs; edit these by hand):"
oc get scc -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.users}{"\n"}{end}' |
  grep ":${NS}:" || true

echo "-- Nodes that are cordoned or carry medik8s/remediation taints (uncordon/untaint by hand):"
oc get nodes \
  -o jsonpath='{range .items[*]}{.metadata.name}{" unschedulable="}{.spec.unschedulable}{" taints="}{.spec.taints[*].key}{"\n"}{end}' |
  grep -E 'unschedulable=true|medik8s|remediation|out-of-service' || true

echo "-- Nodes with medik8s label or annotation keys (inspect: oc get node <name> -o yaml | grep medik8s):"
oc get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.labels}{" "}{.metadata.annotations}{"\n"}{end}' |
  awk '/medik8s/ {print $1}' || true

echo "-- Namespaces:"
oc get namespace "$NS" "$LEASE_NS" --ignore-not-found

echo
echo "Done. Images in your Quay repos and images cached on the nodes are not touched."
