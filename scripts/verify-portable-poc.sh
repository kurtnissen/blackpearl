#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.portable.yaml")
plex_url="${PLEX_URL:-http://localhost:32400}"
token="${PLEX_TOKEN:-}"
movie='/blackpearl/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4'
fixture=/opt/blackpearl/fixtures/blackpearl-poc.mp4

curl_headers=(-H 'Accept: application/xml')
if [[ -n "${token}" ]]; then
  curl_headers+=(-H "X-Plex-Token: ${token}")
fi

curl --fail --silent --show-error http://localhost:8080/readyz >/dev/null

"${compose[@]}" exec -T plex sh -eu -c '
  movie="$1"
  test -r "${movie}"
  test "$(stat -c %s "${movie}")" = 3417699
' verify "${movie}"

sections="$(curl --fail --silent --show-error "${curl_headers[@]}" "${plex_url}/library/sections")"
section_id="$(python3 -c '
import sys
import xml.etree.ElementTree as ET

root = ET.fromstring(sys.stdin.read())
for directory in root.findall("Directory"):
    if directory.get("title") == "BlackPearl POC":
        print(directory.get("key", ""))
        break
' <<<"${sections}")"
if [[ -z "${section_id}" ]]; then
  printf 'Plex library "BlackPearl POC" is not configured. Run scripts/setup-portable-poc.sh first.\n' >&2
  exit 1
fi

catalog="$(curl --fail --silent --show-error "${curl_headers[@]}" "${plex_url}/library/sections/${section_id}/all")"
part_details="$(python3 -c '
import sys
import xml.etree.ElementTree as ET

root = ET.fromstring(sys.stdin.read())
for video in root.findall("Video"):
    if video.get("title") != "BlackPearl POC":
        continue
    part = video.find("./Media/Part")
    if part is not None:
        print("\t".join((part.get("file", ""), part.get("size", ""), part.get("key", ""))))
        break
' <<<"${catalog}")"
if [[ -z "${part_details}" ]]; then
  printf 'Plex has not indexed BlackPearl POC yet.\n' >&2
  exit 1
fi
IFS=$'\t' read -r part_file part_size part_key <<<"${part_details}"
test "${part_file}" = "${movie}"
test "${part_size}" = 3417699
test -n "${part_key}"

temporary_directory="$(mktemp -d)"
cleanup() {
  rm -rf -- "${temporary_directory}"
}
trap cleanup EXIT

range_start=1048576
range_end=1114111
status="$(curl --silent --show-error \
  "${curl_headers[@]}" \
  -H "Range: bytes=${range_start}-${range_end}" \
  --output "${temporary_directory}/plex-range.bin" \
  --write-out '%{http_code}' \
  "${plex_url}${part_key}")"
test "${status}" = 206
test "$(wc -c <"${temporary_directory}/plex-range.bin" | tr -d ' ')" = 65536

source_hash="$("${compose[@]}" exec -T blackpearl sh -eu -c '
  dd if="$1" bs=1 skip="$2" count=65536 status=none | sha256sum | cut -d " " -f 1
' verify "${fixture}" "${range_start}")"
plex_hash="$(shasum -a 256 "${temporary_directory}/plex-range.bin" | cut -d ' ' -f 1)"
test "${plex_hash}" = "${source_hash}"

printf 'portable_blackpearl_ready=PASS\n'
printf 'official_plex_nfs_mount=PASS\n'
printf 'plex_catalog_scan=PASS\n'
printf 'plex_original_media_range=PASS\n'
