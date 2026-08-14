# BlackPearl

BlackPearl is an experimental, open-source Go service that exposes a virtual media library through read-only FUSE or NFS filesystem frontends. Milestone 1 is intentionally narrow: prove that Plex can scan and Direct Play a synthetic MP4 without touching an existing Plex, media, download, or `*arr` path.

> Status: FUSE remains available for native Linux. The portable NFS profile is designed for Docker Desktop and mounts into an unmodified Plex container through Docker's built-in local-volume driver. Provider-backed persistent retention and quota-bounded rolling storage both work in the same binary. macOS Plex Web playback has been verified in both modes.

## What exists today

- One Go 1.24+ binary with modular packages for core, state, PearlFS, PearlCache, Plex, resolver, and acquisition contracts.
- SQLite catalog state and a persistent, content-addressed POC cache.
- Context-aware arbitrary-offset media reads with immutable version validators; callers never receive a cache path.
- Validated movie/episode search intent, a read-only Prowlarr gateway, and provider-neutral release deduplication and ranking.
- Cached-only TorBox acquisition preflights cache availability, enforces `add_only_if_cached=true` during creation, inspects the resulting account object, selects the requested video, and publishes it through the existing atomic manifest transaction.
- A paired localhost acquisition console privately configures Prowlarr, accepts validated movie or TV-episode intent, and returns only the updated public Plex manifest. Provider credentials and release locators never return to the browser.
- Durable Plex Watchlist ingestion observes movies and shows through a bounded, header-authenticated adapter, stores a lease-based SQLite queue, and can serialize cached-only movie acquisition without inventing episode intent for a show.
- Best-effort Plex refresh notifications rescan the exact BlackPearl movie and TV libraries after successful manifest publication without coupling Plex availability to the publication transaction.
- `persistent` and `rolling` configuration modes. Rolling mode fetches strict HTTP ranges into fixed-size chunks, coalesces misses, cancels stale handle-scoped read-ahead after seeks or closes, performs bounded next-episode prefix prefetch, and enforces a hard local byte quota with LRU eviction.
- A generated 8-second H.264/AAC test-pattern MP4 with no third-party media.
- Docker/Compose files for BlackPearl, a legal range-origin fixture, and isolated opt-in Plex acceptance containers.
- Unit, integration, safety, and Linux FUSE smoke tests.
- A portable NFS frontend and macOS Docker Desktop Compose profile that need no
  FUSE mount propagation.

BlackPearl now proves provider-neutral progressive range retrieval and rolling eviction through strict HTTP and TorBox torrent-file gateways. The TorBox profile includes a localhost setup page that discovers eligible completed MP4/MKV files and atomically publishes a searchable manifest of up to 100 selected movies or TV episodes without restarting the stack. Live macOS acceptance has verified a mixed movie/episode manifest, Plex movie and TV scans, Direct Play, non-sequential seeks, restart recovery, seek-aware read-ahead, bounded next-episode prefetch, and continued reads from logical files that never existed completely on BlackPearl's disk. Prowlarr search and cached-only TorBox acquisition are exposed through the tested paired API/UI. The bundled Prowlarr container and BlackPearl connection are live locally; adding private indexers and performing the first authorized live acquisition remain operator steps. The portable profile now observes the isolated Plex server's Watchlist by default through a read-only config-volume mount. Automatic cached-only movie acquisition is implemented but remains disabled by default until authorized indexers are configured and live acceptance is recorded.

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
./scripts/torbox-stack.sh start
```

The launcher opens a locally paired setup page. Paste a TorBox token, search and select one or more completed MP4/MKV files, and choose whether each is a movie or TV episode. Then open Plex at `http://localhost:32402/web` and add:

- a Movies library rooted at `/blackpearl/Movies`; and
- a TV Shows library rooted at `/blackpearl/TV Shows`.

The profile defaults to a 40 GiB rolling cache. A home server can retain every
verified range instead, without waiting for the complete media before playback:

```bash
BLACKPEARL_STORAGE_MODE=persistent ./scripts/torbox-stack.sh start
```

Persistent and rolling chunks use separate namespaces in the same private data
volume. Returning to the normal rolling profile requires only the usual launcher
command with no storage-mode override.

The same stack exposes Prowlarr at `http://localhost:9697`. Complete its
one-time authentication and authorized-indexer setup. In BlackPearl, select
**Find something new**, keep the internal URL `http://prowlarr:9696`, and paste
the API key from Prowlarr Settings. BlackPearl will search and add only a result
that TorBox already reports as cached.

The same isolated profile observes Plex Watchlist movies and shows every 15
minutes. BlackPearl mounts only this stack's named Plex configuration volume,
read-only; it does not inspect a host Plex installation. Observation stores no
Plex token in BlackPearl state and does not acquire anything. The paired setup
page shows only aggregate queue counts and observation health; it never returns
Watchlist titles or identifiers. After authorized indexers are configured and the observe-only counts look correct, automatic
cached-only movie processing can be enabled explicitly with
`BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED=true`. Shows remain observation-only
until an episode policy is configured in a later milestone.

Successful manifest publication also schedules a best-effort refresh of the
exact Plex library roots `/blackpearl/Movies` and `/blackpearl/TV Shows`. The
TorBox Compose profile enables this for its isolated Plex server. Refresh
failures retry in the background and never roll back a published manifest.
Docker Desktop uses the default host endpoint
`http://host.docker.internal:32402`; native Linux deployments should override
`BLACKPEARL_PLEX_REFRESH_URL` with an endpoint reachable from the BlackPearl
container. Windows Docker Desktop and native Linux remain unverified.

The token and manifest are stored with private permissions only inside
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
- BlackPearl does not inspect host media, production Plex, or `*arr` directories. The TorBox profile mounts only its project-scoped Plex config volume read-only for Watchlist authentication.
- Cleanup is guarded to the exact repository runtime root.
- A Plex token is optional, passed by environment, sent as a header, and never written by BlackPearl.

## Storage modes

| Mode | Intended deployment | Milestone 1 behavior |
|---|---|---|
| `persistent` | Home server with multi-TB storage | Local fixture import plus provider-backed, restart-durable chunk retention with no eviction |
| `rolling` | Low-compute VPS with roughly 40-80 GB cache | Strict range fetching, hard quota, coalescing, restart recovery, LRU eviction, playback-aware read-ahead cancellation, and bounded next-episode prefetch |

Both modes use the same range-oriented media-source and filesystem contracts.
Neither mode requires a complete local file before Plex can open, Direct Play,
or seek the logical media.

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
