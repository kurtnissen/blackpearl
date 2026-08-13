#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose_file="${repository_root}/compose.rolling.yaml"
test -f "${compose_file}"
if [[ "${1:-}" == "--remove-data" ]]; then
  docker compose -f "${compose_file}" down --volumes
  printf 'Stopped the rolling POC and removed only its project-scoped Docker volumes.\n'
else
  docker compose -f "${compose_file}" down
  printf 'Stopped the rolling POC; its isolated data volumes were preserved.\n'
fi
