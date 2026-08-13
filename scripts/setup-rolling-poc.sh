#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.rolling.yaml")
plex_url="${PLEX_ROLLING_URL:-http://localhost:32401}"
token="${PLEX_ROLLING_TOKEN:-}"

"${compose[@]}" up --build --force-recreate -d --wait

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

if [[ -z "${section_id}" ]]; then
  response="$(curl --silent --show-error "${headers[@]}" --write-out $'\n%{http_code}' \
    --request POST --get \
    --data-urlencode 'name=BlackPearl Rolling POC' \
    --data-urlencode 'type=movie' \
    --data-urlencode 'agent=tv.plex.agents.movie' \
    --data-urlencode 'scanner=Plex Movie' \
    --data-urlencode 'language=en-US' \
    --data-urlencode 'location=/blackpearl/Movies' \
    "${plex_url}/library/sections")"
  code="$(printf '%s\n' "${response}" | tail -n 1)"
  if [[ "${code}" != "200" && "${code}" != "201" ]]; then
    printf 'Plex rolling library creation requires a claimed server. Open %s/web, sign in, then rerun this script. HTTP %s\n' "${plex_url}" "${code}" >&2
    exit 1
  fi
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
fi

test -n "${section_id}"
curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections/${section_id}/refresh" >/dev/null
for attempt in $(seq 1 60); do
  catalog="$(curl --fail --silent --show-error "${headers[@]}" "${plex_url}/library/sections/${section_id}/all")"
  if grep -q 'title="BlackPearl POC"' <<<"${catalog}"; then
    printf 'BlackPearl rolling POC is running and indexed in Plex library %s.\n' "${section_id}"
    exit 0
  fi
  sleep 1
done
printf 'Plex did not index the rolling POC within 60 seconds.\n' >&2
exit 1
