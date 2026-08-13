#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"

docker compose -f "${repository_root}/compose.portable.yaml" config --format json |
  jq -e '
    ([.services[].volumes[]? | select(.type == "bind")] | length == 0)
    and ([.services[].cap_add[]?] | length == 0)
    and ([.services[].devices[]?] | length == 0)
    and (.services.plex.volumes | any(.target == "/blackpearl" and .read_only == true and .type == "volume"))
    and (.volumes["blackpearl-library"].driver == "local")
    and (.volumes["blackpearl-library"].driver_opts.type == "nfs")
    and ([.services[].ports[]?] | all(.host_ip == "127.0.0.1"))
  ' >/dev/null

printf 'Portable Compose safety checks passed.\n'
