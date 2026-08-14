#!/bin/sh
# Invoked by doc.go's //go:generate directive. The generator lives in the separate `codegen` Go
# module (docs/codegen-client.md explains why: its RFC 8785/gowebpki-jcs dependency must never
# leak into this, the dependency-free, root module), so `go run` can't resolve it as an ordinary
# package path from this directory - this script cd's into that module and calls back with this
# directory's absolute path so -file/-out still resolve relative to here.
set -eu

ORIG="$(pwd)"
cd "$ORIG/../../codegen/cmd/benzene-codegen"
go run . build \
  -file "$ORIG/contracts/payments.spec.json" \
  -out "$ORIG" \
  -mode topic \
  -topics payments:capture
