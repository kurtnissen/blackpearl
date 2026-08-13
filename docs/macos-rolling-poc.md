# macOS Rolling-Cache Plex POC

This profile proves that Plex can play a seekable logical media file whose
complete bytes never exist in BlackPearl's image, mounts, cache, or writable
container layer. It uses only generated test media and isolated Docker volumes.

## Run it

Requirements are Docker Desktop, Docker Compose, and `curl`.

```bash
./scripts/setup-rolling-poc.sh
./scripts/verify-rolling-poc.sh
open http://localhost:32401/web
```

The first run may open Plex's normal sign-in/setup flow. Sign in, keep the
pre-created `BlackPearl Rolling POC` library rooted at `/blackpearl/Movies`, and
finish setup. Rerun both scripts afterward if setup interrupted the initial
scan.

Search for and play `BlackPearl POC`. The eight-second test pattern should play
normally. The automated verifier separately confirms Plex's `Direct play OK`
decision in the server log.

## What the verifier proves

- BlackPearl is ready in rolling mode.
- No complete MP4/MKV exists in the BlackPearl container.
- The logical file is larger than the configured 1 MiB cache.
- Plex indexes the movie through the read-only NFS volume.
- Three non-sequential Plex ranges exactly match the origin.
- Cache chunk plus temporary-fetch bytes stay at or below the quota while the
  entire file is streamed.
- A chunk absent after eviction is fetched from the origin again.
- Plex's media decision is Direct Play.

The separate `range-origin` container deliberately owns the complete legal
fixture. It stands in for a future authorized provider and is not mounted into
BlackPearl.

## Inspect and stop

```bash
docker compose -f compose.rolling.yaml ps
docker compose -f compose.rolling.yaml logs blackpearl
docker compose -f compose.rolling.yaml down
```

Add `-v` to the final command only when you intentionally want to delete this
POC's isolated catalog, rolling-cache, and Plex configuration volumes.

## Ports

- BlackPearl health: `http://localhost:8081/readyz`
- PearlNFS: `localhost:20491`
- Plex Web: `http://localhost:32401/web`

These differ from the persistent POC so both profiles can run side by side.
