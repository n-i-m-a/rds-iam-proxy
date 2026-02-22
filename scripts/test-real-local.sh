#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

ENV_FILE="${ENV_FILE:-scripts/test-real-local.env}"
if [[ ! -f "${ENV_FILE}" ]]; then
  echo "Missing ${ENV_FILE}"
  echo "Create it from scripts/test-real-local.example.env"
  exit 1
fi

# shellcheck disable=SC1090
source "${ENV_FILE}"

PROFILE="${PROFILE:-}"
if [[ -z "${PROFILE}" ]]; then
  echo "PROFILE is required in ${ENV_FILE}"
  exit 1
fi

GO_BIN="${GO_BIN:-go}"
MYSQL_BIN="${MYSQL_BIN:-mysql}"
SQL_QUERY="${SQL_QUERY:-SELECT 1}"
RUN_FULL_PROXY_TEST="${RUN_FULL_PROXY_TEST:-0}"

CONFIG_ARG=()
if [[ -n "${CONFIG_PATH:-}" ]]; then
  CONFIG_ARG=(--config "${CONFIG_PATH}")
fi

resolve_config_for_parse() {
  if [[ -n "${CONFIG_PATH:-}" ]]; then
    echo "${CONFIG_PATH}"
    return 0
  fi
  if [[ -f "./config.yaml" ]]; then
    echo "./config.yaml"
    return 0
  fi
  if [[ -f "${HOME}/.config/rds-iam-proxy/config.yaml" ]]; then
    echo "${HOME}/.config/rds-iam-proxy/config.yaml"
    return 0
  fi
  return 1
}

yaml_profile_value() {
  local config_file="$1"
  local profile_name="$2"
  local key="$3"
  awk -v profile="${profile_name}" -v key="${key}" '
    {
      line=$0
      if (line ~ /^[[:space:]]*-[[:space:]]*name:[[:space:]]*/) {
        sub(/^[[:space:]]*-[[:space:]]*name:[[:space:]]*/, "", line)
        gsub(/^"|"$/, "", line)
        in_profile = (line == profile)
      } else if (in_profile && line ~ ("^[[:space:]]*" key ":[[:space:]]*")) {
        sub("^[[:space:]]*" key ":[[:space:]]*", "", line)
        gsub(/^"|"$/, "", line)
        print line
        exit
      }
    }
  ' "${config_file}"
}

echo "==> Dry-run IAM token generation for profile: ${PROFILE}"
"${GO_BIN}" run ./cmd/rds-iam-proxy "${CONFIG_ARG[@]}" --profile "${PROFILE}" --dry-run

if [[ "${RUN_FULL_PROXY_TEST}" != "1" ]]; then
  echo "==> Dry-run only complete."
  echo "Set RUN_FULL_PROXY_TEST=1 in ${ENV_FILE} to run a real SQL query through local proxy."
  exit 0
fi

if ! command -v "${MYSQL_BIN}" >/dev/null 2>&1; then
  echo "mysql client not found (${MYSQL_BIN}). Install it or set MYSQL_BIN."
  exit 1
fi

# Resolve connection inputs from selected profile in config.
cfg_for_parse="$(resolve_config_for_parse || true)"
if [[ -z "${cfg_for_parse}" ]]; then
  echo "Unable to locate config for profile parsing. Set CONFIG_PATH in ${ENV_FILE}."
  exit 1
fi
listen_addr="$(yaml_profile_value "${cfg_for_parse}" "${PROFILE}" "listen_addr" || true)"
profile_proxy_user="$(yaml_profile_value "${cfg_for_parse}" "${PROFILE}" "proxy_user" || true)"
profile_proxy_password="$(yaml_profile_value "${cfg_for_parse}" "${PROFILE}" "proxy_password" || true)"

PROXY_HOST="${PROXY_HOST:-${listen_addr%:*}}"
PROXY_PORT="${PROXY_PORT:-${listen_addr##*:}}"
PROXY_USER="${PROXY_USER:-${profile_proxy_user}}"
PROXY_PASSWORD="${PROXY_PASSWORD:-${profile_proxy_password}}"

if [[ -z "${PROXY_HOST:-}" || -z "${PROXY_PORT:-}" || -z "${PROXY_USER:-}" || -z "${PROXY_PASSWORD:-}" ]]; then
  echo "Could not auto-resolve proxy connection values from profile ${PROFILE}."
  echo "Set overrides in ${ENV_FILE}: PROXY_HOST, PROXY_PORT, PROXY_USER, PROXY_PASSWORD"
  exit 1
fi

echo "==> Starting proxy in background"
LOG_FILE="$(mktemp)"
"${GO_BIN}" run ./cmd/rds-iam-proxy "${CONFIG_ARG[@]}" --profile "${PROFILE}" >"${LOG_FILE}" 2>&1 &
PROXY_PID=$!

cleanup() {
  if kill -0 "${PROXY_PID}" >/dev/null 2>&1; then
    kill "${PROXY_PID}" >/dev/null 2>&1 || true
    wait "${PROXY_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "==> Waiting for proxy startup"
for _ in $(seq 1 40); do
  if grep -q "proxy listening" "${LOG_FILE}" 2>/dev/null; then
    break
  fi
  sleep 0.25
done
if ! grep -q "proxy listening" "${LOG_FILE}" 2>/dev/null; then
  echo "Proxy did not start in time. Logs:"
  cat "${LOG_FILE}"
  exit 1
fi

echo "==> Executing query via local proxy"
"${MYSQL_BIN}" -h"${PROXY_HOST}" -P"${PROXY_PORT}" -u"${PROXY_USER}" -p"${PROXY_PASSWORD}" -e "${SQL_QUERY}"

echo "==> Real local test passed"
