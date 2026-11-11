#!/usr/bin/env bash
# Synchronize manager RELATED_IMAGE_* values and the FAR build image from operands.yaml.
# The runtime overlays carry static placeholders and are never edited here.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
YQ=${YQ:-bin/yq}; [ -x "$YQ" ] || YQ=yq
refs=$(YQ="$YQ" hack/operand-image-refs.sh "$@")
work=$(mktemp)
trap 'rm -f "$work"' EXIT

set_env() {
  local file=$1
  local query='(select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "manager") | .env[] | select(.name == strenv(WAO_IMAGE_ENV)) | .value)'
  [ "$("$YQ" "$query" "$file")" = "$WAO_IMAGE_REF" ] || "$YQ" -i "$query = strenv(WAO_IMAGE_REF)" "$file"
}

set_far_build_image() {
  local current
  current=$(sed -n 's/^ARG FAR_IMAGE=//p' Dockerfile)
  if [ "$current" != "$WAO_IMAGE_REF" ]; then
    cp -p Dockerfile "$work"
    sed "s|^ARG FAR_IMAGE=.*|ARG FAR_IMAGE=$WAO_IMAGE_REF|" Dockerfile > "$work"
    mv "$work" Dockerfile
  fi
}

while IFS=$'\t' read -r env ref; do
  export WAO_IMAGE_ENV="$env" WAO_IMAGE_REF="$ref"
  set_env config/manager/manager.yaml
  if [ "$env" = "RELATED_IMAGE_FAR_MANAGER" ]; then
    set_far_build_image
  fi
done <<< "$refs"
