#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.rolling.yaml")
plex_url="${PLEX_ROLLING_URL:-http://localhost:32401}"
token="${PLEX_ROLLING_TOKEN:-}"
quota=1048576
movie='/blackpearl/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4'

if [[ -z "${token}" ]]; then
  token="$("${compose[@]}" exec -T plex sh -eu -c '
    preferences="/config/Library/Application Support/Plex Media Server/Preferences.xml"
    if test -r "${preferences}"; then
      sed -n '\''s/.*PlexOnlineToken="\([^\"]*\)".*/\1/p'\'' "${preferences}"
    fi
  ')"
fi
headers=(-H 'Accept: application/xml')
if [[ -n "${token}" ]]; then
  headers+=(-H "X-Plex-Token: ${token}")
fi

curl --fail --silent --show-error http://localhost:8081/readyz >/dev/null
"${compose[@]}" exec -T blackpearl sh -eu -c '
  test -z "$(find / -xdev -type f \( -name "*.mp4" -o -name "*.mkv" \) -print -quit 2>/dev/null)"
  ! mount | grep -q range-origin
'
logical_size="$("${compose[@]}" exec -T plex stat -c %s "${movie}")"
test "${logical_size}" -gt "${quota}"

sections="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections")"
section_id="$(python3 -c '
import sys
import xml.etree.ElementTree as ET
root = ET.fromstring(sys.stdin.read())
for directory in root.findall("Directory"):
    if directory.get("title") == "BlackPearl Rolling POC":
        print(directory.get("key", ""))
        break
' <<<"${sections}")"
test -n "${section_id}"
catalog="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections/${section_id}/all")"
part_key="$(python3 -c '
import sys
import xml.etree.ElementTree as ET
root = ET.fromstring(sys.stdin.read())
for video in root.findall("Video"):
    if video.get("title") == "BlackPearl POC":
        part = video.find("./Media/Part")
        if part is not None:
            print(part.get("key", ""))
        break
' <<<"${catalog}")"
test -n "${part_key}"
rating_key="$(python3 -c '
import sys
import xml.etree.ElementTree as ET
root = ET.fromstring(sys.stdin.read())
for video in root.findall("Video"):
    if video.get("title") == "BlackPearl POC":
        print(video.get("ratingKey", ""))
        break
' <<<"${catalog}")"
test -n "${rating_key}"

temporary_directory="$(mktemp -d)"
cleanup() { rm -rf -- "${temporary_directory}"; }
trap cleanup EXIT

cache_usage() {
  "${compose[@]}" exec -T blackpearl sh -eu -c '
    find /var/lib/blackpearl/cache/rolling -type f \( -name "*.chunk" -o -name ".fetch-*" \) -exec stat -c %s {} + 2>/dev/null | awk "{total += \$1} END {print total + 0}"
  '
}

for start in 0 1310720 3145728; do
  end=$((start + 65535))
  if (( end >= logical_size )); then end=$((logical_size - 1)); fi
  curl --fail --silent --show-error "${headers[@]}" -H "Range: bytes=${start}-${end}" \
    "${plex_url}${part_key}" -o "${temporary_directory}/plex-${start}.bin"
  "${compose[@]}" exec -T range-origin sh -eu -c '
    dd if=/srv/media/blackpearl-poc.mp4 bs=1 skip="$1" count="$2" status=none
  ' verify "${start}" "$((end - start + 1))" >"${temporary_directory}/origin-${start}.bin"
  cmp "${temporary_directory}/origin-${start}.bin" "${temporary_directory}/plex-${start}.bin"
done

curl --fail --silent --show-error "${headers[@]}" "${plex_url}${part_key}" -o /dev/null &
stream_pid=$!
max_cache_bytes=0
while kill -0 "${stream_pid}" 2>/dev/null; do
  cache_bytes="$(cache_usage)"
  if (( cache_bytes > max_cache_bytes )); then max_cache_bytes=${cache_bytes}; fi
  test "${cache_bytes}" -le "${quota}"
  sleep 0.05
done
wait "${stream_pid}"
cache_bytes="$(cache_usage)"
if (( cache_bytes > max_cache_bytes )); then max_cache_bytes=${cache_bytes}; fi
test "${max_cache_bytes}" -le "${quota}"

plex_log='/config/Library/Application Support/Plex Media Server/Logs/Plex Media Server.log'
direct_play_log_offset="$("${compose[@]}" exec -T plex sh -eu -c 'wc -c < "$1"' verify "${plex_log}")"
curl --fail --silent --show-error "${headers[@]}" --get \
	-H 'X-Plex-Client-Identifier: blackpearl-rolling-acceptance' \
	-H 'X-Plex-Product: Plex Web' \
	-H 'X-Plex-Version: 4.156.0' \
	-H 'X-Plex-Platform: Chrome' \
	--data-urlencode 'hasMDE=1' \
	--data-urlencode "path=/library/metadata/${rating_key}" \
	--data-urlencode 'mediaIndex=0' \
	--data-urlencode 'partIndex=0' \
	--data-urlencode 'protocol=hls' \
	--data-urlencode 'fastSeek=1' \
	--data-urlencode 'directPlay=1' \
	--data-urlencode 'directStream=1' \
	--data-urlencode 'directStreamAudio=1' \
	--data-urlencode 'location=lan' \
	--data-urlencode 'session=blackpearl-rolling-acceptance' \
	--data-urlencode 'subtitles=burn' \
	"${plex_url}/video/:/transcode/universal/decision" >/dev/null
"${compose[@]}" exec -T plex sh -eu -c '
  tail -c "+$(( $1 + 1 ))" "$2" | grep -Eq "MDE=1000,Direct play OK.*decision=direct play"
' verify "${direct_play_log_offset}" "${plex_log}"
evicted_index="$("${compose[@]}" exec -T blackpearl sh -eu -c '
  cached="$(find /var/lib/blackpearl/cache/rolling -type f -name "*.chunk" -exec basename {} .chunk \; | sed "s/^0*//" | sed "s/^$/0/" | sort -n)"
  index=0
  while test "$index" -lt 13; do
    if ! printf "%s\n" "$cached" | grep -qx "$index"; then
      printf "%s\n" "$index"
      exit 0
    fi
    index=$((index + 1))
  done
  exit 1
')"
evicted_start=$((evicted_index * 262144))
evicted_end=$((evicted_start + 262143))
origin_pattern="range=\"bytes=${evicted_start}-${evicted_end}\""
origin_before="$("${compose[@]}" logs --no-color range-origin | grep -c "${origin_pattern}" || true)"
"${compose[@]}" up -d --force-recreate blackpearl plex --wait >/dev/null
if [[ -z "${PLEX_ROLLING_TOKEN:-}" ]]; then
  token="$("${compose[@]}" exec -T plex sh -eu -c '
    preferences="/config/Library/Application Support/Plex Media Server/Preferences.xml"
    sed -n '\''s/.*PlexOnlineToken="\([^\"]*\)".*/\1/p'\'' "${preferences}"
  ')"
  headers=(-H 'Accept: application/xml' -H "X-Plex-Token: ${token}")
fi
curl --fail --silent --show-error "${headers[@]}" -H "Range: bytes=${evicted_start}-$((evicted_start + 65535))" "${plex_url}${part_key}" -o /dev/null
origin_after="$("${compose[@]}" logs --no-color range-origin | grep -c "${origin_pattern}" || true)"
test "${origin_after}" -gt "${origin_before}"

printf 'rolling_blackpearl_ready=PASS\n'
printf 'rolling_source_isolated=PASS\n'
printf 'rolling_logical_size_exceeds_quota=PASS\n'
printf 'rolling_plex_catalog_scan=PASS\n'
printf 'rolling_nonsequential_ranges=PASS\n'
printf 'rolling_cache_quota=PASS\n'
printf 'rolling_evicted_range_refetch=PASS\n'
printf 'rolling_plex_direct_play_decision=PASS\n'
