#!/usr/bin/env bash
# Builds all seven custom-handler binaries (orders/payments/shipping/inventory/notifications/
# analytics + mesh) and zips each one together with its own host.json/local Function folders into
# deploy/build/<name>.zip - the Terraform stack's expected input (see variables.tf's *_zip
# defaults) and what `func azure functionapp publish --custom`/`az functionapp deployment source
# config-zip` would also deploy. Run from anywhere; paths below are resolved relative to this
# script. Modeled on examples/aws-lambda-mesh/deploy/build.sh's shape.
#
# Usage: deploy/build.sh [GOOS] [GOARCH]   (default: linux amd64, matching the Linux Consumption
#                                            plan every Function App here targets)
set -euo pipefail

GOOS_ARG="${1:-linux}"
GOARCH_ARG="${2:-amd64}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
OUT_DIR="$SCRIPT_DIR/build"

mkdir -p "$OUT_DIR"

for name in orders payments shipping inventory notifications analytics mesh; do
  echo "building $name ($GOOS_ARG/$GOARCH_ARG)..."
  SERVICE_DIR="$EXAMPLE_DIR/cmd/$name"

  CGO_ENABLED=0 GOOS="$GOOS_ARG" GOARCH="$GOARCH_ARG" go build -o "$SERVICE_DIR/handler" "$SERVICE_DIR"

  ZIP_PATH="$OUT_DIR/$name.zip"
  rm -f "$ZIP_PATH"
  # host.json + every Function folder (function.json) + the handler binary - NOT
  # local.settings.json, which is local-dev only and never deployed.
  (cd "$SERVICE_DIR" && zip -q -r "$ZIP_PATH" host.json handler */function.json)

  rm -f "$SERVICE_DIR/handler"
done

echo "done: $OUT_DIR/{orders,payments,shipping,inventory,notifications,analytics,mesh}.zip"
