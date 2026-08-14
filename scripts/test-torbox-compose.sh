#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_dir}/.." && pwd -P)"
compose_file="${repository_root}/compose.torbox.yaml"
export BLACKPEARL_SETUP_BOOTSTRAP_TOKEN="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

test -f "${compose_file}"
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
assert environment["BLACKPEARL_SETUP_ENABLED"] == "true"
assert environment["BLACKPEARL_SETUP_DIR"] == "/var/lib/blackpearl/setup"
assert environment["BLACKPEARL_SETUP_BOOTSTRAP_TOKEN"] == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
assert "BLACKPEARL_RANGE_OBJECT_ID" not in environment
assert "BLACKPEARL_TORBOX_API_TOKEN_FILE" not in environment
assert "BLACKPEARL_TORBOX_API_TOKEN" not in environment
assert not blackpearl.get("secrets")
assert not model.get("secrets")
assert "BLACKPEARL_RANGE_ORIGIN_URL" not in environment
assert all(volume.get("type") == "volume" for volume in blackpearl.get("volumes", []))
assert not blackpearl.get("devices")
assert not blackpearl.get("cap_add")
assert set(blackpearl["networks"]) == {"blackpearl-control"}
assert set(plex["networks"]) == {"plex-runtime"}
assert set(blackpearl["networks"]).isdisjoint(plex["networks"])
assert blackpearl["healthcheck"]["test"][-1] == "http://localhost:8080/healthz"
for service in services.values():
    for port in service.get("ports", []):
        assert port["host_ip"] == "127.0.0.1"
library = next(volume for volume in plex["volumes"] if volume["target"] == "/blackpearl")
assert library["read_only"] is True
nfs = model["volumes"][library["source"]]
assert nfs["driver_opts"]["type"] == "nfs"
mount_options = nfs["driver_opts"]["o"].split(",")
assert "ro" in mount_options
assert "actimeo=1" in mount_options
assert "lookupcache=none" in mount_options
assert all(not volume.get("source", "").startswith("/") for service in services.values() for volume in service.get("volumes", []))
'

printf 'TorBox Compose safety checks passed.\n'
