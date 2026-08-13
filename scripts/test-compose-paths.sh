#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
runtime_root="${repository_root}/runtime"

model="$(
  cd "${repository_root}"
  docker compose -f compose.yaml -f compose.poc.yaml config --format json
)"

jq -e \
  --arg runtime_root "${runtime_root}/" \
  '
    [.services[].volumes[]? | select(.type == "bind") | .source]
    | all(startswith($runtime_root))
  ' <<<"${model}" >/dev/null

jq -e '
  .services.plex.volumes
  | any(
      .target == "/blackpearl"
      and .read_only == true
      and .bind.propagation == "rslave"
    )
' <<<"${model}" >/dev/null

jq -e '
  (.services.plex.cap_add // []) == []
  and (.services.plex.devices // []) == []
  and (.services.blackpearl.cap_add | index("SYS_ADMIN") != null)
  and (.services.blackpearl.devices | length == 1)
' <<<"${model}" >/dev/null

printf 'Compose safety checks passed.\n'
