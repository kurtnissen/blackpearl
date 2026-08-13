#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
runtime_root="${repository_root}/runtime"
mount_path="${runtime_root}/mount"

case "${runtime_root}" in
  "${repository_root}/runtime") ;;
  *)
    printf 'Refusing cleanup outside the repository runtime root: %s\n' "${runtime_root}" >&2
    exit 1
    ;;
esac

cd "${repository_root}"
docker compose -f compose.yaml -f compose.poc.yaml down

if command -v mountpoint >/dev/null 2>&1 && mountpoint -q "${mount_path}"; then
  sudo umount "${mount_path}"
fi

if [[ "${1:-}" == "--remove-data" ]]; then
  rm -rf -- \
    "${runtime_root}/data" \
    "${runtime_root}/plex-config" \
    "${runtime_root}/transcode"
  mkdir -p "${runtime_root}/mount"
  printf 'Removed generated BlackPearl and isolated Plex POC data.\n'
else
  printf 'Stopped POC and unmounted PearlFS; generated data was preserved.\n'
fi
