#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
runtime_dir="${repository_root}/runtime"
bootstrap_file="${runtime_dir}/torbox-bootstrap"
compose=(docker compose -f "${repository_root}/compose.torbox.yaml")

umask 077
mkdir -p "${runtime_dir}"
if [[ ! -s "${bootstrap_file}" ]]; then
  temporary_file="$(mktemp "${runtime_dir}/.torbox-bootstrap.XXXXXX")"
  trap 'rm -f "${temporary_file:-}"' EXIT
  openssl rand -hex 32 >"${temporary_file}"
  chmod 600 "${temporary_file}"
  mv "${temporary_file}" "${bootstrap_file}"
  trap - EXIT
fi

BLACKPEARL_SETUP_BOOTSTRAP_TOKEN="$(tr -d '\r\n' <"${bootstrap_file}")"
if [[ ! "${BLACKPEARL_SETUP_BOOTSTRAP_TOKEN}" =~ ^[0-9a-f]{64}$ ]]; then
  printf 'BlackPearl setup pairing file is invalid. Remove runtime/torbox-bootstrap and try again.\n' >&2
  exit 1
fi
export BLACKPEARL_SETUP_BOOTSTRAP_TOKEN

BLACKPEARL_STORAGE_MODE="${BLACKPEARL_STORAGE_MODE:-rolling}"
case "${BLACKPEARL_STORAGE_MODE}" in
  rolling)
    BLACKPEARL_CACHE_MAX_BYTES="${BLACKPEARL_CACHE_MAX_BYTES:-42949672960}"
    ;;
  persistent)
    BLACKPEARL_CACHE_MAX_BYTES="${BLACKPEARL_CACHE_MAX_BYTES:-0}"
    ;;
  *)
    printf 'BLACKPEARL_STORAGE_MODE must be rolling or persistent.\n' >&2
    exit 1
    ;;
esac
export BLACKPEARL_STORAGE_MODE BLACKPEARL_CACHE_MAX_BYTES

command="${1:-start}"
case "${command}" in
  start)
    "${compose[@]}" up -d --build --wait
    setup_url="http://localhost:${BLACKPEARL_TORBOX_HTTP_PORT:-8082}/#bootstrap=${BLACKPEARL_SETUP_BOOTSTRAP_TOKEN}"
    if [[ "${BLACKPEARL_NO_OPEN:-0}" != "1" ]] && command -v open >/dev/null 2>&1; then
      open "${setup_url}"
    fi
    printf 'BlackPearl is ready for setup. Paste your TorBox token in the page that opened.\n'
    ;;
  stop)
    "${compose[@]}" down
    ;;
  status)
    "${compose[@]}" ps
    ;;
  logs)
    "${compose[@]}" logs blackpearl
    ;;
  *)
    printf 'Usage: %s {start|stop|status|logs}\n' "$0" >&2
    exit 2
    ;;
esac
