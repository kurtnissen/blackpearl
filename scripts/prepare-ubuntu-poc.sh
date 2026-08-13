#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  printf 'This preparation step requires an Ubuntu/Linux host.\n' >&2
  exit 1
fi
if [[ ! -c /dev/fuse ]]; then
  printf '/dev/fuse is unavailable. Load the fuse kernel module first.\n' >&2
  exit 1
fi
for command in docker findmnt mountpoint sudo; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    printf 'Required command is missing: %s\n' "${command}" >&2
    exit 1
  fi
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
runtime_root="${repository_root}/runtime"
mount_path="${runtime_root}/mount"

mkdir -p \
  "${runtime_root}/data" \
  "${mount_path}" \
  "${runtime_root}/plex-config" \
  "${runtime_root}/transcode"

if ! mountpoint -q "${mount_path}"; then
  sudo mount --bind "${mount_path}" "${mount_path}"
fi
sudo mount --make-shared "${mount_path}"

propagation="$(findmnt -no PROPAGATION --target "${mount_path}")"
if [[ "${propagation}" != "shared" ]]; then
  printf 'Mount propagation is %s, expected shared.\n' "${propagation}" >&2
  exit 1
fi

printf 'Prepared isolated runtime root: %s\n' "${runtime_root}"
