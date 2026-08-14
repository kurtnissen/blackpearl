#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.torbox.yaml")
plex_url="${PLEX_TORBOX_URL:-http://localhost:32402}"
token="${PLEX_TORBOX_TOKEN:-}"

if [[ -z "${BLACKPEARL_TORBOX_API_TOKEN:-}" ]]; then
  printf 'BLACKPEARL_TORBOX_API_TOKEN is required.\n' >&2
  exit 2
fi
if [[ ! "${BLACKPEARL_RANGE_OBJECT_ID:-}" =~ ^[1-9][0-9]*:[1-9][0-9]*$ ]]; then
  printf 'BLACKPEARL_RANGE_OBJECT_ID must use <torrent-id>:<file-id>.\n' >&2
  exit 2
fi

"${compose[@]}" up --build --force-recreate -d --wait

if [[ -z "${token}" ]]; then
  token="$("${compose[@]}" exec -T plex sh -eu -c '
    preferences="/config/Library/Application Support/Plex Media Server/Preferences.xml"
    if test -r "${preferences}"; then
      sed -n '\''s/.*PlexOnlineToken="\([^"]*\)".*/\1/p'\'' "${preferences}"
    fi
  ')"
fi

headers=(-H 'Accept: application/xml')
if [[ -n "${token}" ]]; then
  headers+=(-H "X-Plex-Token: ${token}")
fi

sections="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections")"
section_id="$(python3 -c '
import sys
import xml.etree.ElementTree as ET
root = ET.fromstring(sys.stdin.read())
for directory in root.findall("Directory"):
    if directory.get("title") == "BlackPearl TorBox POC":
        print(directory.get("key", ""))
        break
' <<<"${sections}")"

if [[ -z "${section_id}" ]]; then
  response="$(curl --silent --show-error "${headers[@]}" --write-out $'\n%{http_code}' \
    --request POST --get \
    --data-urlencode 'name=BlackPearl TorBox POC' \
    --data-urlencode 'type=movie' \
    --data-urlencode 'agent=tv.plex.agents.movie' \
    --data-urlencode 'scanner=Plex Movie' \
    --data-urlencode 'language=en-US' \
    --data-urlencode 'location=/blackpearl/Movies' \
    "${plex_url}/library/sections")"
  code="$(printf '%s\n' "${response}" | tail -n 1)"
  if [[ "${code}" != "200" && "${code}" != "201" ]]; then
    printf 'Plex library creation requires a claimed server. Open %s/web, sign in, then rerun. HTTP %s\n' "${plex_url}" "${code}" >&2
    exit 1
  fi
  sections="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections")"
  section_id="$(python3 -c '
import sys
import xml.etree.ElementTree as ET
root = ET.fromstring(sys.stdin.read())
for directory in root.findall("Directory"):
    if directory.get("title") == "BlackPearl TorBox POC":
        print(directory.get("key", ""))
        break
' <<<"${sections}")"
fi

test -n "${section_id}"
curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections/${section_id}/refresh" >/dev/null
for _ in $(seq 1 90); do
  catalog="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections/${section_id}/all")"
  if grep -q 'title="BlackPearl POC"' <<<"${catalog}"; then
    printf 'BlackPearl TorBox POC is running and indexed in Plex library %s.\n' "${section_id}"
    exit 0
  fi
  sleep 1
done
printf 'Plex did not index the TorBox POC within 90 seconds.\n' >&2
exit 1
