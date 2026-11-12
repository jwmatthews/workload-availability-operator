#!/bin/bash -e
# Fail unless the active Go toolchain is the one go.mod selects.
want=$(sed -n 's/^toolchain //p' "$(dirname "$0")/../go.mod")
got=$(go env GOVERSION)
if [ -z "$want" ] || [ "$got" != "$want" ]; then
  echo "Go toolchain is ${got}, go.mod selects ${want:-<none>}" >&2
  exit 1
fi
echo "Go toolchain ${got} matches go.mod"
