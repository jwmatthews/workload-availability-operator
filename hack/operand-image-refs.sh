#!/usr/bin/env bash
# Validate operands.yaml image settings and their manager.yaml destinations offline, then emit
# ENV<TAB>repository:tag. Source settings are the copy script's concern and are not checked here.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
YQ=${YQ:-bin/yq}; [ -x "$YQ" ] || YQ=yq
keys=("$@"); [ ${#keys[@]} -gt 0 ] || keys=(nhc snr sbr far mdr nmo)
seen=" "
for key in "${keys[@]}"; do
  case "$key" in nhc|snr|sbr|far|mdr|nmo) ;; *) echo "unknown operand: $key" >&2; exit 1 ;; esac
  case "$seen" in *" $key "*) echo "duplicate operand: $key" >&2; exit 1 ;; esac
  seen+="$key "
done
work=$(mktemp); trap 'rm -f "$work"' EXIT

# Validate the six-operand contract and the image fields; the image set itself is checked in Go
# against internal/images.Required.
"$YQ" -o=json '.' operands.yaml | jq -er --args '
  if (.operands | type) != "object" or (.operands | keys) != ["far", "mdr", "nhc", "nmo", "sbr", "snr"]
      then error("operands.yaml must contain exactly nhc, snr, sbr, far, mdr, nmo") else . end
  | [.operands | to_entries[] | .key as $key | .value
      | if (.images | type) == "object" and (.images | length) > 0 then .
        else error($key + ": no images") end
      | .images | to_entries[] | {key: $key, env: .key, image: .value}]
  | (group_by(.env) | map(select(length > 1)) | first) as $duplicate
  | if $duplicate != null then error($duplicate[0].key + "/" + $duplicate[0].env + ": image environment-variable names must be unique") else . end
  | map(. as $entry | ($entry.key + "/" + $entry.env + ": ") as $where
      | if (.image.repository | type) != "string" then error($where + "missing repository")
        elif (.image.repository | test("^[A-Za-z0-9]+([._-]+[A-Za-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-]+[a-z0-9]+)*)*$") | not)
          or ((.image.repository | contains(":")) and (.image.repository | contains("/") | not))
          then error($where + "repository must be an image path without a tag or digest")
        elif (.image.tag | type) != "string" then error($where + "missing nonempty tag")
        elif (.image.tag | test("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$") | not)
          then error($where + "invalid image tag")
        else . end)
  | sort_by(.env)[] | select(.key as $key | $ARGS.positional | index($key))
  | [.key, .env, .image.repository, .image.tag] | @tsv
' "${keys[@]}" > "$work"

require_one() {
  local file=$1 query=$2 count
  count=$("$YQ" -o=json '.' "$file" | jq -s "$query | length")
  [ "$count" = 1 ] || { echo "$key/$env: expected exactly one image destination in $file" >&2; exit 1; }
}

# Preflight selected image destinations before emitting records or allowing any mutation.
while IFS=$'\t' read -r key env repository tag; do
  export WAO_IMAGE_ENV="$env"
  require_one config/manager/manager.yaml \
    '[.[] | select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "manager") | .env[] | select(.name == env.WAO_IMAGE_ENV)]'
  if [ "$env" = "RELATED_IMAGE_FAR_MANAGER" ]; then
    count=$(grep -c '^ARG FAR_IMAGE=' Dockerfile || true)
    [ "$count" = 1 ] || { echo "$key/$env: expected exactly one ARG FAR_IMAGE= in Dockerfile" >&2; exit 1; }
  fi
done < "$work"
while IFS=$'\t' read -r key env repository tag; do
  printf '%s\t%s:%s\n' "$env" "$repository" "$tag"
done < "$work"
