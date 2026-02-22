#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INVOCATION_DIR="$(pwd)"

if [[ "${1:-}" == "" ]]; then
  echo "Usage:"
  echo "  bash scripts/run.sh --profile <name> [extra flags]"
  echo "  bash scripts/run.sh --profiles <a,b> [extra flags]"
  echo "  bash scripts/run.sh --all-profiles [extra flags]"
  exit 1
fi

ARGS=("$@")
HAS_CONFIG_FLAG=false
CONFIG_FROM_FLAG=""
EXPECT_CONFIG_VALUE=false

for arg in "${ARGS[@]}"; do
  if [[ "${EXPECT_CONFIG_VALUE}" == "true" ]]; then
    CONFIG_FROM_FLAG="${arg}"
    EXPECT_CONFIG_VALUE=false
    continue
  fi

  case "${arg}" in
    --config)
      HAS_CONFIG_FLAG=true
      EXPECT_CONFIG_VALUE=true
      ;;
    --config=*)
      HAS_CONFIG_FLAG=true
      CONFIG_FROM_FLAG="${arg#--config=}"
      ;;
  esac
done

if [[ "${HAS_CONFIG_FLAG}" == "false" ]]; then
  CANDIDATE_CWD="${INVOCATION_DIR}/config.yaml"
  CANDIDATE_ROOT="${ROOT_DIR}/config.yaml"
  CANDIDATE_ROOT_PARENT="$(cd "${ROOT_DIR}/.." && pwd)/config.yaml"
  CANDIDATE_HOME="${HOME}/.config/rds-iam-proxy/config.yaml"
  RESOLVED_CONFIG=""
  RESOLVED_SOURCE=""

  if [[ -f "${CANDIDATE_CWD}" ]]; then
    RESOLVED_CONFIG="${CANDIDATE_CWD}"
    RESOLVED_SOURCE="current directory"
  elif [[ -f "${CANDIDATE_ROOT}" ]]; then
    RESOLVED_CONFIG="${CANDIDATE_ROOT}"
    RESOLVED_SOURCE="repository root"
  elif [[ -f "${CANDIDATE_ROOT_PARENT}" ]]; then
    RESOLVED_CONFIG="${CANDIDATE_ROOT_PARENT}"
    RESOLVED_SOURCE="repository parent directory"
  elif [[ -f "${CANDIDATE_HOME}" ]]; then
    RESOLVED_CONFIG="${CANDIDATE_HOME}"
    RESOLVED_SOURCE="home config"
  fi

  if [[ "${RESOLVED_CONFIG}" != "" ]]; then
    echo "Using config: ${RESOLVED_CONFIG} (${RESOLVED_SOURCE})"
    cd "${ROOT_DIR}"
    exec go run ./cmd/rds-iam-proxy --config "${RESOLVED_CONFIG}" "${ARGS[@]}"
  fi

  echo "No config found in defaults; starting without --config"
  cd "${ROOT_DIR}"
  exec go run ./cmd/rds-iam-proxy "${ARGS[@]}"
fi

if [[ "${CONFIG_FROM_FLAG}" != "" ]]; then
  echo "Using config: ${CONFIG_FROM_FLAG} (from --config)"
else
  echo "Using config: (from --config)"
fi
cd "${ROOT_DIR}"
exec go run ./cmd/rds-iam-proxy "${ARGS[@]}"
