#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.portable.yaml")
plex_url="${PLEX_URL:-http://localhost:32400}"
token="${PLEX_TOKEN:-}"

curl_headers=(-H 'Accept: application/xml')
if [[ -n "${token}" ]]; then
  curl_headers+=(-H "X-Plex-Token: ${token}")
fi

# Recreate both services together so Plex receives a fresh NFS mount whenever
# the development BlackPearl image is rebuilt. The POC NFS handler intentionally
# keeps file handles in memory; preserving Plex across a server replacement
# would otherwise leave the client with stale handles.
"${compose[@]}" up --build --force-recreate -d --wait

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
  library_created=no
  for attempt in $(seq 1 120); do
    response="$(curl --silent --show-error \
      "${curl_headers[@]}" \
      --write-out $'\n%{http_code}' \
      --request POST \
      --get \
      --data-urlencode 'name=BlackPearl POC' \
      --data-urlencode 'type=movie' \
      --data-urlencode 'agent=tv.plex.agents.movie' \
      --data-urlencode 'scanner=Plex Movie' \
      --data-urlencode 'language=en-US' \
      --data-urlencode 'location=/blackpearl/Movies' \
      "${plex_url}/library/sections")"
    http_code="$(printf '%s\n' "${response}" | tail -n 1)"
    response_body="$(printf '%s\n' "${response}" | sed '$d')"
    if [[ "${http_code}" == "200" || "${http_code}" == "201" ]]; then
      library_created=yes
      break
    fi
    if [[ "${http_code}" == "400" && "${response_body}" == *"the server is still starting up"* ]]; then
      sleep 1
      continue
    fi
    printf 'Plex library creation failed with HTTP %s: %s\n' "${http_code}" "${response_body}" >&2
    exit 1
  done
  if [[ "${library_created}" != "yes" ]]; then
    printf 'Plex did not finish library startup within 120 seconds.\n' >&2
    exit 1
  fi
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
fi

if [[ -z "${section_id}" ]]; then
  printf 'Plex did not create the isolated BlackPearl POC library.\n' >&2
  exit 1
fi

curl --fail --silent --show-error \
  "${curl_headers[@]}" \
  --request GET \
  "${plex_url}/library/sections/${section_id}/refresh" >/dev/null

for attempt in $(seq 1 45); do
  catalog="$(curl --fail --silent --show-error "${curl_headers[@]}" "${plex_url}/library/sections/${section_id}/all")"
  indexed="$(python3 -c '
import sys
import xml.etree.ElementTree as ET

root = ET.fromstring(sys.stdin.read())
print("yes" if any(video.get("title") == "BlackPearl POC" for video in root.findall("Video")) else "no")
' <<<"${catalog}")"
  if [[ "${indexed}" == "yes" ]]; then
    printf 'BlackPearl POC is running and indexed in Plex library %s.\n' "${section_id}"
    exit 0
  fi
  sleep 1
done

printf 'Plex did not index BlackPearl POC within 45 seconds.\n' >&2
exit 1
