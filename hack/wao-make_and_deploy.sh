#!/usr/bin/env bash
# Stop on the first failed command, on unset variables, and on failures inside pipelines.
set -euo pipefail

# Run from the repo root no matter where the script was invoked from.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

export REG="${REG:-ttl.sh/$(uuidgen | tr '[:upper:]' '[:lower:]')}"
export VERSION="${VERSION:-5.1.0-$(date -u +%Y%m%d%H%M%S)}"
export IMG="${IMG:-$REG/workload-availability-operator:v$VERSION}"
export BUNDLE_IMG="${BUNDLE_IMG:-$REG/workload-availability-operator-bundle:v$VERSION}"
export CATALOG_IMG="${CATALOG_IMG:-$REG/workload-availability-operator-catalog:v$VERSION}"

# Confirm KUBECONFIG reaches the cluster before spending time on builds.
if ! oc whoami; then
  echo "ERROR: 'oc whoami' failed with KUBECONFIG=$KUBECONFIG" >&2
  exit 1
fi
echo "Using cluster: $(oc whoami --show-server)"

make docker-build
make docker-push

make bundle-build
make bundle-push

grep -n 'workload-availability-operator:' bundle/manifests/*.clusterserviceversion.yaml

make catalog-build
make catalog-push

grep -n '^schema:\|^image:\|^name:' catalog/index.yaml

make catalog-source                # writes dist/catalogsource.yaml with your CATALOG_IMG
cat dist/catalogsource.yaml
oc apply -f dist/catalogsource.yaml

# When the tag is reused, the CatalogSource spec is "unchanged" and OLM keeps the old pod.
# Deleting it forces a fresh pull of the image just pushed.
oc -n openshift-marketplace delete pod -l olm.catalogSource=wao-poc-catalog --ignore-not-found

echo "Waiting for the catalog to serve..."
oc -n openshift-marketplace wait catalogsource/wao-poc-catalog \
  --for=jsonpath='{.status.connectionState.lastObservedState}'=READY --timeout=5m

echo "Waiting for the package to appear..."
until oc -n openshift-marketplace get packagemanifest workload-availability-operator >/dev/null 2>&1; do
  sleep 5
done

# Namespace, OperatorGroup and Subscription (installPlanApproval: Automatic)
oc apply -f config/catalog/subscription.yaml

NS=openshift-workload-availability
echo "Waiting for OLM to install the operator..."
oc -n "$NS" wait subscription/workload-availability-operator \
  --for=jsonpath='{.status.state}'=AtLatestKnown --timeout=10m
CSV=$(oc -n "$NS" get subscription workload-availability-operator -o jsonpath='{.status.installedCSV}')
oc -n "$NS" wait "csv/$CSV" --for=jsonpath='{.status.phase}'=Succeeded --timeout=10m

oc -n "$NS" get csv,deploy,pods
oc get workloadavailabilityconfig cluster

