#!/usr/bin/env bash
# Offline generated consistency and RBAC/bundle shape checks; no network or cluster.
# Regenerates in place to detect drift, then restores the original files when they differ, so the
# working tree is left as it was found. Bundle freshness is checked separately by make test.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
YQ=${YQ:-bin/yq}; [ -x "$YQ" ] || YQ=yq
KUSTOMIZE=${KUSTOMIZE:-bin/kustomize}; [ -x "$KUSTOMIZE" ] || KUSTOMIZE=kustomize
fail=0; problem() { echo "verify-operand-manifests: $*" >&2; fail=1; }
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

# 1. Validate the YAML and selected image destinations; local checkouts are not needed.
YQ="$YQ" hack/operand-image-refs.sh >/dev/null

# 2. Regeneration reproduces the current files, including unstaged operand updates.
# Compare contents rather than the Git index so local updates can be verified before committing;
# on drift, put the pre-regeneration copy back and report.
generated=(internal/operands/manifests config/operands config/manager/manager.yaml)
for path in "${generated[@]}"; do
  if [ -e "$path" ]; then
    mkdir -p "$work/$(dirname "$path")"
    cp -R "$path" "$work/$path"
  fi
done
make -s operand-manifests
for path in "${generated[@]}"; do
  if ! diff -qr "$work/$path" "$path"; then
    problem "generated files differ in $path; run make operand-manifests and review the changes"
    rm -rf "$path"; cp -R "$work/$path" "$path"
  fi
done

# 3. RELATED_IMAGE_* equal the validated deployment references.
refs=$(YQ="$YQ" hack/operand-image-refs.sh)
while IFS=$'\t' read -r env want; do
  got=$("$YQ" "select(.kind == \"Deployment\") | .spec.template.spec.containers[0].env[] | select(.name == \"$env\") | .value" config/manager/manager.yaml)
  [ "$want" = "$got" ] || problem "manager.yaml $env=$got, operands.yaml reference is $want"
  if [ "$env" = "RELATED_IMAGE_FAR_MANAGER" ]; then
    got=$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.initContainers[] | select(.name == "far-agent-inventory") | .image' config/manager/manager.yaml)
    [ "$want" = "$got" ] || problem "manager.yaml FAR inventory image=$got, operands.yaml reference is $want"
  fi
done <<< "$refs"

# 4. Every operand ServiceAccount is in exactly one of OPERAND_SERVICE_ACCOUNTS (roles folded into
# the CSV) or OPERAND_STANDALONE_SERVICE_ACCOUNTS (ServiceAccount, ClusterRole and binding shipped as
# standalone bundle objects under their operand names; see the Makefile comment).
extra=$(sed -n 's/^OPERAND_SERVICE_ACCOUNTS := //p' Makefile | tr ',' '\n')
standalone=$(sed -n 's/^OPERAND_STANDALONE_SERVICE_ACCOUNTS := //p' Makefile | tr ',' '\n')
for sa in $standalone; do
  ! grep -qx "$sa" <<<"$extra" || problem "ServiceAccount $sa is in both OPERAND_SERVICE_ACCOUNTS and OPERAND_STANDALONE_SERVICE_ACCOUNTS"
  found=
  for f in bundle/manifests/*.yaml; do
    if [ "$("$YQ" '.kind + "/" + .metadata.name' "$f")" = "ServiceAccount/$sa" ]; then found=1; fi
  done
  [ -n "$found" ] || problem "standalone ServiceAccount $sa is not a file in bundle/manifests; run make bundle-reset"
done
for k in nhc snr sbr far mdr nmo; do
  errf=$(mktemp)
  # LoadRestrictionsNone: config/operands/sbr/overlays/rbac lists a copied file that SBR's own kustomization omits.
  if ! rbac=$("$KUSTOMIZE" build --load-restrictor LoadRestrictionsNone "config/operands/$k/overlays/rbac" 2>"$errf"); then
    problem "$k: kustomize build config/operands/$k/overlays/rbac failed: $(tail -1 "$errf")"; rm -f "$errf"; continue
  fi
  rm -f "$errf"
  kinds=$(printf '%s\n' "$rbac" | "$YQ" -N '.kind' | grep -v -E '^(ServiceAccount|Role|RoleBinding|ClusterRole|ClusterRoleBinding)$' || true)
  [ -z "$kinds" ] || problem "$k: config/operands/$k/overlays/rbac emits non-RBAC kinds: $(tr '\n' ' ' <<<"$kinds")"
  sas=$(printf '%s\n' "$rbac" | "$YQ" -N 'select(.kind == "ServiceAccount") | .metadata.name')
  [ -n "$sas" ] || problem "$k: no ServiceAccount in config/operands/$k/overlays/rbac"
  for sa in $sas; do
    grep -qx "$sa" <<<"$extra" || grep -qx "$sa" <<<"$standalone" || problem "$k: ServiceAccount $sa is not in OPERAND_SERVICE_ACCOUNTS; its roles would become standalone bundle files"
  done
done

# 4b. NHC enables metrics mTLS only when its ServiceAccount can read the client CA in
# kube-system/extension-apiserver-authentication; the copied OCP overlay grants it through a
# kube-system Role whose binding config/operands/nhc/overlays/rbac points at the operand ServiceAccount, so
# operator-sdk folds it into the CSV and OLM promotes it cluster-wide (AllNamespaces).
nhc_rbac=$("$KUSTOMIZE" build config/operands/nhc/overlays/rbac 2>/dev/null || true)
ca_roles=$(printf '%s\n' "$nhc_rbac" | "$YQ" -N 'select(.kind == "Role" and ([.rules[] | select(.resources[] == "configmaps") | .resourceNames[]?] | contains(["extension-apiserver-authentication"]))) | .metadata.name')
[ -n "$ca_roles" ] || problem "nhc: no Role grants node-healthcheck-controller-manager read of configmaps/extension-apiserver-authentication, so NHC cannot enable metrics mTLS"
for r in $ca_roles; do
  bound=$(printf '%s\n' "$nhc_rbac" | "$YQ" -N "select(.kind == \"RoleBinding\" and .roleRef.name == \"$r\") | .subjects[] | select(.kind == \"ServiceAccount\") | .name")
  grep -qx node-healthcheck-controller-manager <<<"$bound" || problem "nhc: Role $r (client CA extension-apiserver-authentication) is bound to [$(tr '\n' ' ' <<<"$bound")], not node-healthcheck-controller-manager"
done

# 5. The bundle carries no standalone Role or RoleBinding, only the SBR agent's ServiceAccount and
# ClusterRoleBinding, and only known ClusterRoles.
for f in bundle/manifests/*.yaml; do
  kind=$("$YQ" '.kind' "$f"); name=$("$YQ" '.metadata.name' "$f")
  case "$kind/$name" in
    ClusterServiceVersion/*|CustomResourceDefinition/*) ;;
    ServiceAccount/sbr-operator-sbr-agent|ClusterRoleBinding/sbr-operator-sbr-agent-rolebinding) ;;
    ClusterRole/sbr-operator-sbr-agent-role|ClusterRole/sbr-operator-sbr-agent-privileged-scc) ;;
    ClusterRole/*-metrics-reader|ClusterRole/*-ext-remediation|ClusterRole/*-admin-role|ClusterRole/*-editor-role|ClusterRole/*-viewer-role) ;;
    Role/node-healthcheck-prometheus-k8s|RoleBinding/node-healthcheck-prometheus-k8s) ;; # NHC OCP packaging: platform Prometheus discovery in the operand namespace
    *) problem "unexpected standalone $kind $name in $f" ;;
  esac
done
# The SBR manager creates ClusterRoleBindings whose roleRef names these ClusterRoles verbatim
# (internal/controller/storagebasedremediationconfig_controller.go in the SBR source); without them, enabling
# SBR leaves its agent with no permissions and no privileged SCC.
for cr in sbr-operator-sbr-agent-role sbr-operator-sbr-agent-privileged-scc; do
  found=
  for f in bundle/manifests/*.yaml; do
    if [ "$("$YQ" '.kind + "/" + .metadata.name' "$f")" = "ClusterRole/$cr" ]; then found=1; fi
  done
  [ -n "$found" ] || problem "bundle/manifests has no ClusterRole $cr, which the SBR manager binds by name"
done
crds=$(ls bundle/manifests/*.yaml | xargs -n1 "$YQ" '.kind' | grep -c CustomResourceDefinition || true)
[ "$crds" = 13 ] || problem "bundle has $crds CRDs, want 13"
# 5b. The WAO CSV carries what NHC's OCP bundle carries: the client CA rule for the manager and the
# console plugin annotation that lets OLM offer enabling the plugin at install.
csv=$(ls bundle/manifests/*.clusterserviceversion.yaml)
rule=$("$YQ" '[.spec.install.spec.permissions[] | select(.serviceAccountName == "node-healthcheck-controller-manager") | .rules[] | select(.resources[] == "configmaps") | .resourceNames[]?] | contains(["extension-apiserver-authentication"])' "$csv")
[ "$rule" = true ] || problem "bundle CSV: node-healthcheck-controller-manager permissions lack configmaps/extension-apiserver-authentication; run make bundle-reset"
plugins=$("$YQ" '.metadata.annotations."console.openshift.io/plugins" // ""' "$csv")
grep -q node-remediation-console-plugin <<<"$plugins" || problem "bundle CSV: annotation console.openshift.io/plugins does not list node-remediation-console-plugin"

[ $fail = 0 ] && echo "verify-operand-manifests: OK"
exit $fail
