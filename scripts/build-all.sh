#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

OUTPUT_DIR="${OUTPUT_DIR:-dist}"

# "All distros" in Go means building for target OS/arch combinations.
# Linux binaries generally work across most distros (glibc/musl caveats if cgo is enabled).
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

mkdir -p "${OUTPUT_DIR}"

LDFLAGS="-s -w"
COMMON_FILES=(
  "config.example.yaml"
  "README.md"
  "LICENSE"
  "THIRD_PARTY_NOTICES.md"
)

echo "==> Building rds-iam-proxy binaries"
for target in "${TARGETS[@]}"; do
  IFS="/" read -r GOOS GOARCH <<<"${target}"
  ext=""
  if [[ "${GOOS}" == "windows" ]]; then
    ext=".exe"
  fi

  dir_name="rds-iam-proxy_${GOOS}_${GOARCH}"
  target_dir="${OUTPUT_DIR}/${dir_name}"
  rm -rf "${target_dir}"
  mkdir -p "${target_dir}"

  out="${target_dir}/rds-iam-proxy${ext}"
  echo " - ${GOOS}/${GOARCH} -> ${out}"
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" ./cmd/rds-iam-proxy

  for f in "${COMMON_FILES[@]}"; do
    cp "${f}" "${target_dir}/"
  done

  mkdir -p "${target_dir}/certs"

  archive="${OUTPUT_DIR}/rds-iam-proxy_${GOOS}_${GOARCH}.tar.gz"
  tar -C "${OUTPUT_DIR}" -czf "${archive}" "${dir_name}"
done

echo "==> Done. Artifacts in ${OUTPUT_DIR}/"
