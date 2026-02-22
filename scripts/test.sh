#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

echo "==> Formatting Go files"
gofmt -w ./cmd ./internal

echo "==> Running go test"
go test ./...

echo "==> OK"
