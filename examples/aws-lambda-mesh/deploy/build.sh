#!/usr/bin/env bash
# Builds all seven Lambda binaries (orders/payments/shipping/inventory/notifications/analytics +
# mesh) for the provided.al2023 custom runtime and zips each into deploy/build/<name>.zip - the
# Terraform stack's expected input (see variables.tf's *_zip defaults). Run from anywhere; paths
# below are resolved relative to this script.
#
# Usage: deploy/build.sh [GOARCH]   (default: arm64, matching variables.tf's lambda_architecture default)
set -euo pipefail

ARCH="${1:-arm64}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
OUT_DIR="$SCRIPT_DIR/build"

mkdir -p "$OUT_DIR"

for name in orders payments shipping inventory notifications analytics mesh; do
  echo "building $name ($ARCH)..."
  BUILD_DIR="$(mktemp -d)"
  trap 'rm -rf "$BUILD_DIR"' EXIT

  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$BUILD_DIR/bootstrap" "$EXAMPLE_DIR/cmd/$name"

  ZIP_PATH="$OUT_DIR/$name.zip"
  rm -f "$ZIP_PATH"
  (cd "$BUILD_DIR" && zip -q "$ZIP_PATH" bootstrap)

  rm -rf "$BUILD_DIR"
  trap - EXIT
done

echo "done: $OUT_DIR/{orders,payments,shipping,inventory,notifications,analytics,mesh}.zip"
