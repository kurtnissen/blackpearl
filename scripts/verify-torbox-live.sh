#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"

token="${BLACKPEARL_TORBOX_API_TOKEN:-}"
object_id="${BLACKPEARL_RANGE_OBJECT_ID:-}"

if [[ -z "${token}" ]]; then
  printf 'BLACKPEARL_TORBOX_API_TOKEN is required for the opt-in live check.\n' >&2
  exit 2
fi
if [[ ! "${object_id}" =~ ^[1-9][0-9]*:[1-9][0-9]*$ ]]; then
  printf 'BLACKPEARL_RANGE_OBJECT_ID must use <torrent-id>:<file-id>.\n' >&2
  exit 2
fi

cd "${repository_root}"
BLACKPEARL_TORBOX_LIVE=1 go test ./internal/gateway/torbox \
  -run '^TestLiveAuthorizedTorrentRanges$' -count=1 >/dev/null

printf 'torbox_live_metadata=PASS\n'
printf 'torbox_live_ranges=PASS\n'
