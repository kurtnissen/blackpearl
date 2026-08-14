#!/usr/bin/env bash
set -euo pipefail

minimum="${BLACKPEARL_MIN_COVERAGE:-80.0}"
profile="${1:-coverage.out}"

if [[ ! -f "${profile}" ]]; then
  printf 'Coverage profile does not exist: %s\n' "${profile}" >&2
  exit 1
fi

filtered_profile="$(mktemp "${TMPDIR:-/tmp}/blackpearl-coverage.XXXXXX")"
trap 'rm -f "${filtered_profile}"' EXIT

# Bun dependencies can contain unrelated Go source. The Go package wildcard
# discovers that generated dependency tree after `bun install`, but the
# BlackPearl coverage floor applies only to this repository's Go code.
awk 'NR == 1 || $0 !~ /\/web\/node_modules\//' "${profile}" >"${filtered_profile}"

total="$(go tool cover -func="${filtered_profile}" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')"
if [[ -z "${total}" ]]; then
  printf 'Could not read total coverage from %s\n' "${profile}" >&2
  exit 1
fi

awk -v total="${total}" -v minimum="${minimum}" 'BEGIN {
  if (total + 0 < minimum + 0) {
    printf "Coverage %.1f%% is below required %.1f%%.\n", total, minimum > "/dev/stderr"
    exit 1
  }
  printf "Coverage %.1f%% meets required %.1f%%.\n", total, minimum
}'
