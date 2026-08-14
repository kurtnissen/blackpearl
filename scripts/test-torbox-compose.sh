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
assert set(services) == {"blackpearl", "plex", "prowlarr"}
blackpearl = services["blackpearl"]
plex = services["plex"]
prowlarr = services["prowlarr"]
environment = blackpearl["environment"]
assert blackpearl["build"]["target"] == "runtime"
assert environment["BLACKPEARL_STORAGE_MODE"] == "rolling"
assert str(environment["BLACKPEARL_CACHE_READ_AHEAD_CHUNKS"]) == "8"
assert str(environment["BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS"]) == "16"
assert environment["BLACKPEARL_RANGE_PROVIDER"] == "torbox-torrent"
assert environment["BLACKPEARL_SETUP_ENABLED"] == "true"
assert environment["BLACKPEARL_SETUP_DIR"] == "/var/lib/blackpearl/setup"
assert environment["BLACKPEARL_SETUP_BOOTSTRAP_TOKEN"] == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
assert environment["BLACKPEARL_WATCHLIST_ENABLED"] == "true"
assert environment["BLACKPEARL_WATCHLIST_PREFERENCES_PATH"] == "/plex-config/Library/Application Support/Plex Media Server/Preferences.xml"
assert environment["BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED"] == "false"
assert environment["BLACKPEARL_PLEX_REFRESH_ENABLED"] == "true"
assert environment["BLACKPEARL_PLEX_REFRESH_URL"] == "http://host.docker.internal:32402"
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
assert any(str(host).startswith("host.docker.internal") for host in blackpearl.get("extra_hosts", []))
assert set(prowlarr["networks"]) == {"blackpearl-control"}
assert set(plex["networks"]) == {"plex-runtime"}
assert set(blackpearl["networks"]).isdisjoint(plex["networks"])
assert set(prowlarr["networks"]).isdisjoint(plex["networks"])
assert prowlarr["image"] == "lscr.io/linuxserver/prowlarr:latest"
assert any(volume["target"] == "/config" and volume["type"] == "volume" for volume in prowlarr["volumes"])
blackpearl_plex_config = next(volume for volume in blackpearl["volumes"] if volume["target"] == "/plex-config")
plex_config = next(volume for volume in plex["volumes"] if volume["target"] == "/config")
assert blackpearl_plex_config["type"] == "volume"
assert blackpearl_plex_config["read_only"] is True
assert blackpearl_plex_config["source"] == plex_config["source"]
assert not plex_config.get("read_only", False)
assert blackpearl["healthcheck"]["test"][-1] == "http://localhost:8080/healthz"
for service in services.values():
    for port in service.get("ports", []):
        assert port["host_ip"] == "127.0.0.1"
assert any(port["target"] == 9696 for port in prowlarr["ports"])
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

BLACKPEARL_STORAGE_MODE=persistent BLACKPEARL_CACHE_MAX_BYTES=0 \
  docker compose -f "${compose_file}" config --format json |
  python3 -c '
import json
import sys

model = json.load(sys.stdin)
blackpearl = model["services"]["blackpearl"]
environment = blackpearl["environment"]
assert environment["BLACKPEARL_STORAGE_MODE"] == "persistent"
assert str(environment["BLACKPEARL_CACHE_MAX_BYTES"]) == "0"
assert set(blackpearl["networks"]) == {"blackpearl-control"}
assert set(model["services"]["plex"]["networks"]) == {"plex-runtime"}
assert set(blackpearl["networks"]).isdisjoint(model["services"]["plex"]["networks"])
assert all(volume.get("type") == "volume" for volume in blackpearl.get("volumes", []))
assert not blackpearl.get("devices")
assert not blackpearl.get("cap_add")
'

printf 'TorBox Compose safety checks passed.\n'
