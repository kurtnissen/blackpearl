#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
"${script_dir}/torbox-stack.sh" start
printf 'BlackPearl is running. Paste your TorBox token and choose one or more videos.\n'
printf 'Then open Plex at http://localhost:32402/web and scan /blackpearl/Movies.\n'
printf 'For episodes, add a TV Shows library rooted at /blackpearl/TV Shows.\n'
