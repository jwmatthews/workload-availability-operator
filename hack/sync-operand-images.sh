#!/usr/bin/env bash
# Synchronize RELATED_IMAGE_* values in config/manager/manager.yaml from operands.yaml.
# RELATED_IMAGE_FAR_MANAGER also sets the image of the far-agent-inventory init container.
# The runtime overlays carry static placeholders and are never edited here.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
YQ=${YQ:-bin/yq}; [ -x "$YQ" ] || YQ=yq
refs=$(YQ="$YQ" hack/operand-image-refs.sh "$@")

set_env() {
  local file=$1
  local query='(select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "manager") | .env[] | select(.name == strenv(WAO_IMAGE_ENV)) | .value)'
  [ "$("$YQ" "$query" "$file")" = "$WAO_IMAGE_REF" ] || "$YQ" -i "$query = strenv(WAO_IMAGE_REF)" "$file"
}

set_far_init_image() {
  local file=$1
  local query='(select(.kind == "Deployment") | .spec.template.spec.initContainers[] | select(.name == "far-agent-inventory") | .image)'
  [ "$("$YQ" "$query" "$file")" = "$WAO_IMAGE_REF" ] || "$YQ" -i "$query = strenv(WAO_IMAGE_REF)" "$file"
}

while IFS=$'\t' read -r env ref; do
  export WAO_IMAGE_ENV="$env" WAO_IMAGE_REF="$ref"
  set_env config/manager/manager.yaml
  [ "$env" != RELATED_IMAGE_FAR_MANAGER ] || set_far_init_image config/manager/manager.yaml
done <<< "$refs"
