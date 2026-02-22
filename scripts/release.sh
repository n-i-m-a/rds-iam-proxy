#!/usr/bin/env bash
# Tag and push to trigger release workflow. Creates GitHub Release with assets.
# Usage: ./scripts/release.sh v1.0.0
set -euo pipefail
if [[ -z "${1:-}" ]]; then
  echo "Usage: $0 <tag>" >&2
  echo "Example: $0 v1.0.0" >&2
  exit 1
fi
tag="$1"
git tag "$tag"
git push origin "$tag"
