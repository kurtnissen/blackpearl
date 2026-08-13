#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose_file="${repository_root}/compose.rolling.yaml"

test -f "${compose_file}"
docker compose -f "${compose_file}" config --format json |
  python3 -c '
import json
import sys

model = json.load(sys.stdin)
services = model["services"]
assert set(services) == {"blackpearl", "plex", "range-origin"}
origin = services["range-origin"]
blackpearl = services["blackpearl"]
plex = services["plex"]
assert not origin.get("ports")
assert not origin.get("volumes")
assert blackpearl["build"]["target"] == "runtime"
assert all(volume.get("type") == "volume" for volume in blackpearl.get("volumes", []))
assert not blackpearl.get("devices")
assert not blackpearl.get("cap_add")
assert blackpearl["environment"]["BLACKPEARL_STORAGE_MODE"] == "rolling"
assert str(blackpearl["environment"]["BLACKPEARL_CACHE_MAX_BYTES"]) == "1048576"
assert str(blackpearl["environment"]["BLACKPEARL_CACHE_CHUNK_BYTES"]) == "262144"
for service in (blackpearl, plex):
    for port in service.get("ports", []):
        assert port["host_ip"] == "127.0.0.1"
library = next(volume for volume in plex["volumes"] if volume["target"] == "/blackpearl")
assert library["read_only"] is True
nfs = model["volumes"][library["source"]]
assert nfs["driver_opts"]["type"] == "nfs"
assert "ro" in nfs["driver_opts"]["o"].split(",")
assert all(not volume.get("source", "").startswith("/") for service in services.values() for volume in service.get("volumes", []))
'

printf 'Rolling Compose safety checks passed.\n'
