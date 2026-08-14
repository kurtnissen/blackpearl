#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.torbox.yaml")
"${compose[@]}" up --build --force-recreate -d --wait
printf 'BlackPearl is running. Open http://localhost:8082, paste your TorBox token, and choose a video.\n'
printf 'Then open Plex at http://localhost:32402/web and scan /blackpearl/Movies.\n'
