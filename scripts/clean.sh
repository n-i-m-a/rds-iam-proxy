#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

OUTPUT_DIR="${OUTPUT_DIR:-dist}"

echo "==> Removing build artifacts"
rm -rf "${OUTPUT_DIR}"

echo "==> Clean complete"
