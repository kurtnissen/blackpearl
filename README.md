# BlackPearl

BlackPearl is an experimental, open-source Go service that exposes a virtual media library through read-only FUSE or NFS filesystem frontends. Milestone 1 is intentionally narrow: prove that Plex can scan and Direct Play a synthetic MP4 without touching an existing Plex, media, download, or `*arr` path.

> Status: FUSE remains available for native Linux. The portable NFS profile is designed for Docker Desktop and mounts into an unmodified Plex container through Docker's built-in local-volume driver. Persistent and quota-bounded rolling storage both work in the same binary. macOS Plex Web playback has been verified in both modes.

## What exists today

- One Go 1.24+ binary with modular packages for core, state, PearlFS, PearlCache, Plex, resolver, and acquisition contracts.
- SQLite catalog state and a persistent, content-addressed POC cache.
- Context-aware arbitrary-offset media reads with immutable version validators; callers never receive a cache path.
- `persistent` and `rolling` configuration modes. Rolling mode fetches strict HTTP ranges into fixed-size chunks, coalesces misses, and enforces a hard local byte quota with LRU eviction.
- A generated 8-second H.264/AAC test-pattern MP4 with no third-party media.
- Docker/Compose files for BlackPearl, a legal range-origin fixture, and isolated opt-in Plex acceptance containers.
- Unit, integration, safety, and Linux FUSE smoke tests.
- A portable NFS frontend and macOS Docker Desktop Compose profile that need no
  FUSE mount propagation.

BlackPearl now proves provider-neutral progressive range retrieval and rolling eviction through strict HTTP and TorBox torrent-file gateways. The TorBox profile includes a localhost setup page that discovers eligible completed MP4/MKV files and activates one selection without restarting the stack. TorBox support is read-only; it does not yet implement read-ahead, Prowlarr, Usenet, or automatic torrent creation.

## Architecture at a glance

```text
Plex -> PearlFS (native Linux) ----+
                                   +-> Core catalog -> MediaSource.ReadAt(ctx, bytes, offset)
Plex -> PearlNFS (portable Docker) +          |                    |
                                              v                    v
                                            SQLite       persistent cache
                                                                or
                                                      rolling chunk cache
                                                                |
                                                        RangeSource gateway
```

`Media.Size` is the logical size shown to Plex. It does not mean the whole object exists locally. A media record holds a provider/object reference, and the common read handle supports arbitrary offsets. See [docs/architecture.md](docs/architecture.md).

## Local development

Requirements: Go 1.24+, Docker with Compose, `jq`, and standard Unix tooling.

```bash
make verify
make compose-check
make docker-poc
```

The normal Compose file runs only BlackPearl. It binds only repository-owned directories under `runtime/`:

```bash
mkdir -p runtime/data runtime/mount
docker compose up --build
```

A real kernel FUSE mount requires Linux, `/dev/fuse`, and the container privileges declared in `compose.yaml`.

## macOS portable Plex POC

Docker Desktop on macOS can use the NFS profile without host mount propagation:

```bash
./scripts/setup-portable-poc.sh
./scripts/verify-portable-poc.sh
open http://localhost:32400/web
```

The scripts launch an isolated official Plex container, create the
`BlackPearl POC` library, and verify that Plex serves an arbitrary source range
unchanged. Follow [the macOS runbook](docs/macos-plex-poc.md) for the final play,
seek, and Direct Play dashboard check.

## macOS rolling Plex POC

The rolling profile keeps the complete test MP4 outside BlackPearl and exposes
the 3.4 MB logical file through a 1 MiB cache:

```bash
./scripts/setup-rolling-poc.sh
./scripts/verify-rolling-poc.sh
open http://localhost:32401/web
```

The verifier checks source isolation, Plex indexing, exact non-sequential
ranges, live cache occupancy including temporary fetches, eviction/refetch, and
Plex's `Direct play OK` decision. See [the rolling runbook](docs/macos-rolling-poc.md).

## TorBox provider

TorBox can replace the synthetic HTTP origin while retaining the same rolling
cache and filesystem path. Start the isolated browser-first profile:

```bash
docker compose -f compose.torbox.yaml up -d --build --wait
open http://localhost:8082
```

Paste a TorBox token, choose one completed MP4 or MKV, then open Plex at
`http://localhost:32402/web` and add a Movies library rooted at
`/blackpearl/Movies`. The token is stored with private permissions only inside
the named BlackPearl data volume. It is never returned to the browser after
save and is never written to SQLite, cache filenames, container environment,
logs, or telemetry.

The direct CLI probe remains available for provider debugging:

```bash
BLACKPEARL_TORBOX_API_TOKEN='your token' \
BLACKPEARL_RANGE_OBJECT_ID='torrent-id:file-id' \
./scripts/verify-torbox-live.sh
```

See [the macOS TorBox runbook](docs/macos-torbox-poc.md) for object selection,
cache sizing, Plex setup, and cleanup.

## Ubuntu Plex POC

Use a fresh Ubuntu Server and the isolated acceptance stack. Do not point these files at production media or Plex configuration.

```bash
cp .env.example .env
./scripts/prepare-ubuntu-poc.sh
docker compose -f compose.yaml -f compose.poc.yaml up --build -d
./scripts/verify-fuse.sh
```

Then open Plex at `http://YOUR_UBUNTU_SERVER_IP:32400/web`, add one Movies library rooted at `/blackpearl/Movies`, scan it, and play `BlackPearl POC (2026)`. Confirm Direct Play and seek behavior in the Plex dashboard. Full instructions and cleanup are in [docs/ubuntu-plex-poc.md](docs/ubuntu-plex-poc.md).

## Safety

- All supplied host binds stay under this repository's ignored `runtime/` directory.
- The Plex container receives the propagated media mount read-only and receives no FUSE device or elevated capability.
- BlackPearl does not inspect host media, Plex, or `*arr` directories; TorBox discovery is an explicit read-only setup action.
- Cleanup is guarded to the exact repository runtime root.
- A Plex token is optional, passed by environment, sent as a header, and never written by BlackPearl.

## Storage modes

| Mode | Intended deployment | Milestone 1 behavior |
|---|---|---|
| `persistent` | Home server with multi-TB storage | Implemented for the local synthetic fixture |
| `rolling` | Low-compute VPS with roughly 40-80 GB cache | Implemented POC with strict range fetching, hard quota, coalescing, restart recovery, and LRU eviction |

Both modes use the same FUSE and range-oriented media-source contract. Plex Direct Play is a primary target for the later rolling deployment.

## Project documents

- [Architecture](docs/architecture.md)
- [Ubuntu Plex POC runbook](docs/ubuntu-plex-poc.md)
- [macOS Docker Desktop Plex POC](docs/macos-plex-poc.md)
- [macOS rolling-cache Plex POC](docs/macos-rolling-poc.md)
- [macOS TorBox Plex POC](docs/macos-torbox-poc.md)
- [Portable filesystem evaluation](docs/portability-filesystem-evaluation.md)
- [Acceptance criteria and evidence](docs/acceptance-evidence.md)
- [Detailed Milestone 1 design](docs/superpowers/specs/2026-08-13-milestone-1-fuse-plex-design.md)
- [Implementation plan](docs/superpowers/plans/2026-08-13-milestone-1-fuse-plex.md)

## License

BlackPearl is available under the [MIT License](LICENSE).
