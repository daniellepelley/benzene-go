#!/bin/sh
# Prints the `/...` package pattern for every module in go.work, space-separated on one line.
#
# This repo is a go.work workspace, and `./...` does not cross a nested module boundary even in
# workspace mode - so `go test ./...` from the root silently tests the root module and nothing else.
# Every go command that should cover the repo has to name every module, which is why this script
# exists: one derived list, used by CI and by anyone working here, instead of a hand-written list
# that goes stale without saying so. It did go stale - nine modules had fallen out of it, and a test
# in examples/azure-functions-mesh asserted a contract direction the spec had already inverted while
# CI stayed green.
#
#   go vet   $(scripts/modules.sh)
#   go build $(scripts/modules.sh)
#   go test  $(scripts/modules.sh) -race -cover
set -eu

cd "$(dirname "$0")/.."

mods=$(go work edit -json | jq -r '.Use[].DiskPath' | sed 's:/*$::' | sed 's:$:/...:' | tr '\n' ' ')
mods=${mods% }

if [ -z "$mods" ]; then
  echo "no modules found in go.work" >&2
  exit 1
fi

printf '%s\n' "$mods"
