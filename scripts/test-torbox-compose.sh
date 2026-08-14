#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose_file="${repository_root}/compose.torbox.yaml"

test -f "${compose_file}"
BLACKPEARL_TORBOX_API_TOKEN=test-token \
BLACKPEARL_RANGE_OBJECT_ID=17:3 \
  docker compose -f "${compose_file}" config --format json |
  python3 -c '
import json
import sys

model = json.load(sys.stdin)
services = model["services"]
assert set(services) == {"blackpearl", "plex"}
blackpearl = services["blackpearl"]
plex = services["plex"]
environment = blackpearl["environment"]
assert blackpearl["build"]["target"] == "runtime"
assert environment["BLACKPEARL_STORAGE_MODE"] == "rolling"
assert environment["BLACKPEARL_RANGE_PROVIDER"] == "torbox-torrent"
assert environment["BLACKPEARL_RANGE_OBJECT_ID"] == "17:3"
assert environment["BLACKPEARL_TORBOX_API_TOKEN_FILE"] == "/run/secrets/torbox_api_token"
assert "BLACKPEARL_TORBOX_API_TOKEN" not in environment
assert any(secret["target"] == "/run/secrets/torbox_api_token" for secret in blackpearl["secrets"])
assert model["secrets"]["torbox_api_token"]["environment"] == "BLACKPEARL_TORBOX_API_TOKEN"
assert "BLACKPEARL_RANGE_ORIGIN_URL" not in environment
assert all(volume.get("type") == "volume" for volume in blackpearl.get("volumes", []))
assert not blackpearl.get("devices")
assert not blackpearl.get("cap_add")
for service in services.values():
    for port in service.get("ports", []):
        assert port["host_ip"] == "127.0.0.1"
library = next(volume for volume in plex["volumes"] if volume["target"] == "/blackpearl")
assert library["read_only"] is True
nfs = model["volumes"][library["source"]]
assert nfs["driver_opts"]["type"] == "nfs"
assert "ro" in nfs["driver_opts"]["o"].split(",")
assert all(not volume.get("source", "").startswith("/") for service in services.values() for volume in service.get("volumes", []))
'

printf 'TorBox Compose safety checks passed.\n'
