#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose=(docker compose -f "${repository_root}/compose.yaml" -f "${repository_root}/compose.poc.yaml")
fixture=/opt/blackpearl/fixtures/blackpearl-poc.mp4
virtual='/mnt/blackpearl/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4'

curl --fail --silent --show-error http://localhost:8080/readyz >/dev/null

"${compose[@]}" exec -T blackpearl sh -eu -c '
  fixture="$1"
  virtual="$2"
  test -r "${virtual}"
  test "$(sha256sum "${fixture}" | cut -d " " -f 1)" = "$(sha256sum "${virtual}" | cut -d " " -f 1)"
  fixture_range="$(dd if="${fixture}" bs=1 skip=4096 count=8192 2>/dev/null | sha256sum | cut -d " " -f 1)"
  virtual_range="$(dd if="${virtual}" bs=1 skip=4096 count=8192 2>/dev/null | sha256sum | cut -d " " -f 1)"
  test "${fixture_range}" = "${virtual_range}"
' verify "${fixture}" "${virtual}"

printf 'PearlFS exact-byte and offset-read checks passed.\n'
