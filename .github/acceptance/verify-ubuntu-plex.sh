#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/../.." && pwd -P)"
compose=(
  docker compose
  -f "${repository_root}/compose.yaml"
  -f "${repository_root}/compose.poc.yaml"
  -f "${repository_root}/.github/acceptance/compose.ubuntu-plex.yaml"
)
fixture=/opt/blackpearl/fixtures/blackpearl-poc.mp4
virtual='/blackpearl/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4'
plex_url=http://localhost:32400

wait_for_http() {
  local url="$1"
  local attempts="$2"
  local delay="$3"
  local attempt
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if curl --fail --silent --show-error "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep "${delay}"
  done
  printf 'Timed out waiting for %s\n' "${url}" >&2
  return 1
}

wait_for_http http://localhost:8080/readyz 60 2
wait_for_http "${plex_url}/identity" 90 2

"${repository_root}/scripts/verify-fuse.sh"
"${compose[@]}" exec -T plex test -r "${virtual}"
if "${compose[@]}" exec -T plex sh -c 'touch "$1"' verify "${virtual}" 2>/dev/null; then
  printf 'Plex media mount unexpectedly accepted a write.\n' >&2
  exit 1
fi

sections_file="$(mktemp)"
library_file="$(mktemp)"
decision_file="$(mktemp)"
range_file="$(mktemp)"
fixture_range_file="$(mktemp)"
create_response_file="$(mktemp)"
trap 'rm -f "${sections_file}" "${library_file}" "${decision_file}" "${range_file}" "${fixture_range_file}" "${create_response_file}"' EXIT

create_status=""
for _ in $(seq 1 90); do
  create_status="$(curl --silent --show-error --request POST --get \
    --data-urlencode 'name=BlackPearl POC' \
    --data-urlencode 'type=movie' \
    --data-urlencode 'agent=tv.plex.agents.movie' \
    --data-urlencode 'scanner=Plex Movie' \
    --data-urlencode 'language=en-US' \
    --data-urlencode 'location=/blackpearl/Movies' \
    --output "${create_response_file}" \
    --write-out '%{http_code}' \
    "${plex_url}/library/sections")"
  if [[ "${create_status}" =~ ^2[0-9][0-9]$ ]]; then
    break
  fi
  if [[ "${create_status}" == 400 ]] && grep -Fq 'the server is still starting up' "${create_response_file}"; then
    sleep 2
    continue
  fi
  printf 'Plex library creation returned HTTP %s: ' "${create_status}" >&2
  head -c 4096 "${create_response_file}" >&2
  printf '\n' >&2
  exit 1
done
if [[ ! "${create_status}" =~ ^2[0-9][0-9]$ ]]; then
  printf 'Plex did not finish starting before the library-creation deadline.\n' >&2
  exit 1
fi

curl --fail --silent --show-error "${plex_url}/library/sections" >"${sections_file}"
section_key="$(xmllint --xpath "string(//Directory[@title='BlackPearl POC']/@key)" "${sections_file}")"
if [[ ! "${section_key}" =~ ^[0-9]+$ ]]; then
  printf 'Plex did not create the isolated BlackPearl POC library.\n' >&2
  exit 1
fi
curl --fail --silent --show-error "${plex_url}/library/sections/${section_key}/refresh" >/dev/null

rating_key=""
for _ in $(seq 1 90); do
  curl --fail --silent --show-error "${plex_url}/library/sections/${section_key}/all" >"${library_file}"
  rating_key="$(xmllint --xpath "string(//Video[contains(@title, 'BlackPearl POC')]/@ratingKey)" "${library_file}")"
  if [[ "${rating_key}" =~ ^[0-9]+$ ]]; then
    break
  fi
  sleep 2
done
if [[ ! "${rating_key}" =~ ^[0-9]+$ ]]; then
  printf 'Plex did not index BlackPearl POC (2026).\n' >&2
  exit 1
fi

metadata_file="$(mktemp)"
trap 'rm -f "${sections_file}" "${library_file}" "${decision_file}" "${range_file}" "${fixture_range_file}" "${create_response_file}" "${metadata_file}"' EXIT
curl --fail --silent --show-error "${plex_url}/library/metadata/${rating_key}" >"${metadata_file}"
part_key="$(xmllint --xpath 'string(//Part/@key)' "${metadata_file}")"
logical_size="$(xmllint --xpath 'string(//Part/@size)' "${metadata_file}")"
if [[ "${part_key}" != /library/parts/* || ! "${logical_size}" =~ ^[0-9]+$ ]]; then
  printf 'Plex indexed metadata without a readable media part.\n' >&2
  exit 1
fi

range_start=1048576
range_end=1114111
curl --fail --silent --show-error \
  --header "Range: bytes=${range_start}-${range_end}" \
  --output "${range_file}" \
  "${plex_url}${part_key}"
"${compose[@]}" exec -T blackpearl sh -c \
  'dd if="$1" bs=1 skip="$2" count="$3" 2>/dev/null' \
  verify "${fixture}" "${range_start}" "$((range_end - range_start + 1))" >"${fixture_range_file}"
if [[ "$(sha256sum "${range_file}" | cut -d ' ' -f 1)" != "$(sha256sum "${fixture_range_file}" | cut -d ' ' -f 1)" ]]; then
  printf 'Plex range bytes did not match the generated fixture.\n' >&2
  exit 1
fi

curl --fail --silent --show-error --get \
  --header 'X-Plex-Product: Plex Web' \
  --header 'X-Plex-Version: 4.156.0' \
  --header 'X-Plex-Platform: Chrome' \
  --header 'X-Plex-Platform-Version: 151.0' \
  --header 'X-Plex-Device: Linux' \
  --header 'X-Plex-Model: bundled' \
  --header 'X-Plex-Provides: player' \
  --header 'X-Plex-Client-Identifier: blackpearl-ubuntu-acceptance' \
  --data-urlencode "path=/library/metadata/${rating_key}" \
  --data-urlencode 'mediaIndex=0' \
  --data-urlencode 'partIndex=0' \
  --data-urlencode 'protocol=hls' \
  --data-urlencode 'hasMDE=1' \
  --data-urlencode 'fastSeek=1' \
  --data-urlencode 'directPlay=1' \
  --data-urlencode 'directStream=1' \
  --data-urlencode 'videoQuality=100' \
  --data-urlencode 'videoResolution=1920x1080' \
  --data-urlencode 'maxVideoBitrate=200000' \
  --data-urlencode 'location=lan' \
  --data-urlencode 'audioBoost=100' \
  --data-urlencode 'subtitleSize=100' \
  --output "${decision_file}" \
  "${plex_url}/video/:/transcode/universal/decision"
decision="$(xmllint --xpath 'string(//Part/@decision)' "${decision_file}")"
if [[ "${decision}" != directplay && "${decision}" != 'direct play' ]]; then
  printf 'Plex Web profile did not select Direct Play; decision=%s\n' "${decision}" >&2
  exit 1
fi

printf 'Ubuntu Plex acceptance passed: section=%s ratingKey=%s size=%s decision=%s\n' \
  "${section_key}" "${rating_key}" "${logical_size}" "${decision}"
