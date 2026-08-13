#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose_file="${repository_root}/compose.portable.yaml"

if [[ ! -f "${compose_file}" ]]; then
  printf 'Refusing cleanup because the portable Compose file is missing: %s\n' "${compose_file}" >&2
  exit 1
fi

compose=(docker compose -f "${compose_file}")
if [[ "${1:-}" == "--remove-data" ]]; then
  "${compose[@]}" down --volumes --remove-orphans
  printf 'Stopped the portable POC and removed only its project-scoped Docker volumes.\n'
else
  "${compose[@]}" down --remove-orphans
  printf 'Stopped the portable POC; BlackPearl and isolated Plex state were preserved.\n'
fi
