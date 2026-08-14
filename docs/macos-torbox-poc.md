# macOS TorBox Plex POC

This profile connects BlackPearl's rolling range cache to one already-complete
MP4 in your TorBox account, exposes it through PearlNFS, and lets an isolated
Plex container scan and Direct Play the logical file. It does not search for,
create, alter, or delete TorBox downloads.

## Before you run it

Use only media you are authorized to access. For this POC, select a directly
playable MP4 rather than an archive or a different container format. In TorBox,
find the torrent ID and the file ID shown by the account torrent-list API. The
BlackPearl object ID is those positive numbers joined by a colon.

Export the credentials only in the terminal session that launches the stack:

```bash
export BLACKPEARL_TORBOX_API_TOKEN='your token'
export BLACKPEARL_RANGE_OBJECT_ID='torrent-id:file-id'
```

BlackPearl keeps the API token and temporary CDN links out of SQLite, cache
paths, application logs, telemetry, and the container environment. Compose
creates a read-only in-memory secret from the shell variable and mounts it at
`/run/secrets/torbox_api_token`; do not commit the token to an env file.

## Run it

First prove direct API and range access, then launch the Plex stack:

```bash
./scripts/verify-torbox-live.sh
./scripts/setup-torbox-poc.sh
open http://localhost:32402/web
```

If Plex asks you to sign in, finish its normal setup and rerun the setup script.
The script creates an isolated movie library named `BlackPearl TorBox POC` at
`/blackpearl/Movies` and waits for `BlackPearl POC` to be indexed.

The default cache limit is 40 GiB. Override it for a smaller test before launch,
for example `export BLACKPEARL_CACHE_MAX_BYTES=4294967296` for 4 GiB. The logical
file may be larger than this limit; BlackPearl stores only requested fixed-size
chunks and evicts old chunks as needed.

## Inspect and stop

```bash
docker compose -f compose.torbox.yaml ps
docker compose -f compose.torbox.yaml logs blackpearl
docker compose -f compose.torbox.yaml down
```

Add `-v` only when you intentionally want to delete this POC's isolated catalog,
rolling cache, and Plex configuration volumes.

Ports are isolated from the existing POCs:

- BlackPearl health: `http://localhost:8082/readyz`
- PearlNFS: `localhost:20492`
- Plex Web: `http://localhost:32402/web`

Passing the direct probe proves live TorBox metadata and range reads. Plex scan
and playback are separate manual acceptance evidence; do not call those passed
until Plex has actually indexed and played the item.
