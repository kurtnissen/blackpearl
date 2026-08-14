# BlackPearl

BlackPearl is an experimental, open-source Go service that exposes a virtual media library through read-only FUSE or NFS filesystem frontends. Milestone 1 is intentionally narrow: prove that Plex can scan and Direct Play a synthetic MP4 without touching an existing Plex, media, download, or `*arr` path.

> Status: FUSE remains available for native Linux. The portable NFS profile is designed for Docker Desktop and mounts into an unmodified Plex container through Docker's built-in local-volume driver. Provider-backed persistent retention and quota-bounded rolling storage both work in the same binary. macOS Plex Web playback has been verified in both modes.

## What exists today

- One Go 1.24+ binary with modular packages for core, state, PearlFS, PearlCache, Plex, resolver, and acquisition contracts.
- SQLite catalog state and a persistent, content-addressed POC cache.
- Context-aware arbitrary-offset media reads with immutable version validators; callers never receive a cache path.
- Validated movie/episode search intent, a read-only Prowlarr gateway, and provider-neutral release deduplication and ranking. Auxiliary trailers, teasers, samples, previews, and featurettes are rejected before cache planning; up to 100 eligible hashes are cache-probed before the durable five-candidate fallback plan is capped.
- Cached-only TorBox acquisition preflights cache availability, enforces `add_only_if_cached=true` during creation, inspects the resulting account object, selects the requested video, and publishes it through the existing atomic manifest transaction.
- Explicit uncached preparation persists a redacted SQLite job, verifies transient torrent metadata against the ranked release fingerprint, reconciles provider mutations, survives restarts, reports monotonic provider-neutral preparation progress, and publishes only after TorBox exposes playable media.
- The optional Internet Archive adapter gives the legal POC two bounded, verified open-media paths: exact licensed MP4/MKV files can become durable arbitrary-range candidates immediately, while verified torrent metadata remains a fallback for TorBox preparation. Direct objects are represented by opaque provider IDs, never arbitrary URLs, and are never deleted by BlackPearl.
- A paired localhost acquisition console privately configures Prowlarr, accepts validated movie or TV-episode intent, and returns only the updated public Plex manifest. Provider credentials and release locators never return to the browser.
- Durable Plex Watchlist ingestion observes movies and shows through a bounded, header-authenticated adapter and stores a lease-based SQLite queue. Opted-in movies enter the restart-safe acquisition queue; an independent `pilot` policy starts a newly observed show at `S01E01`, then advances its exact durable frontier one metadata-resolved episode only after qualifying playback. It never authorizes a season or series.
- Best-effort Plex refresh notifications rescan the exact BlackPearl movie and TV libraries after successful manifest publication without coupling Plex availability to the publication transaction.
- `persistent` and `rolling` configuration modes. Rolling mode fetches strict HTTP ranges into fixed-size chunks, coalesces misses, cancels stale handle-scoped read-ahead after seeks or closes, performs bounded next-episode prefix prefetch, and enforces a hard local byte quota with LRU eviction.
- A generated 8-second H.264/AAC test-pattern MP4 with no third-party media.
- Docker/Compose files for BlackPearl, a legal range-origin fixture, and isolated opt-in Plex acceptance containers.
- Unit, integration, safety, and Linux FUSE smoke tests.
- A portable NFS frontend and macOS Docker Desktop Compose profile that need no
  FUSE mount propagation.

BlackPearl now proves provider-neutral progressive range retrieval and rolling eviction through strict HTTP direct-file and TorBox torrent-file gateways. The TorBox profile includes a localhost setup page that discovers eligible completed MP4/MKV files and atomically publishes a searchable manifest of up to 100 selected movies or TV episodes without restarting the stack. Live macOS acceptance has verified a mixed movie/episode manifest, Plex movie and TV scans, Direct Play, non-sequential seeks, restart recovery, seek-aware read-ahead, bounded next-episode prefetch, and continued reads from logical files that never existed completely on BlackPearl's disk. Cached and explicit background TorBox acquisition are exposed through the paired API/UI. A licensed *MariposaHD* S01E01 exact-file request completed without TorBox preparation, published a 175,099,607-byte logical MP4, Direct Played in Plex with forward/backward seeking, and survived a BlackPearl-only restart while retaining only 34 requested 1 MiB chunks. Legal torrent fallbacks for *Tears of Steel (2012)* and *House on Haunted Hill (1959)* have also completed through TorBox with partial rolling storage. The portable profile observes the isolated Plex server's Watchlist by default through a read-only config-volume mount. Automatic Watchlist acquisition and the independent S01E01 show-pilot policy remain disabled by default until authorized sources are configured.

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
that TorBox already reports as cached. When the top-ranked result is not cached,
the setup page offers an explicit **Prepare through TorBox** action. That action
creates a restart-safe background job and never stores provider credentials or
release locators in SQLite.

For a legal smoke test, the Compose profile also enables a direct public
Internet Archive search adapter. Exact licensed MP4/MKV files can be persisted
as metadata-only range candidates, including their immutable content validator,
and published without a complete local copy or a TorBox mutation. A changed
same-name/same-size remote file is rejected before publication and advances to
the next durable candidate. If no eligible direct file is available, bounded torrent
metadata can still be verified against its selected info hash before TorBox
creation. Candidate ordering is cached torrent, direct range, then uncached
torrent, with one direct slot reserved in the five-entry durable plan. Set
`BLACKPEARL_OPEN_MEDIA_SEARCH_ENABLED=false` to disable this adapter.

The same isolated profile observes Plex Watchlist movies and shows every 15
minutes. BlackPearl mounts only this stack's named Plex configuration volume,
read-only; it does not inspect a host Plex installation. Observation stores no
Plex token in BlackPearl state and does not acquire anything. The paired setup
page shows only aggregate queue counts and observation health; it never returns
Watchlist titles or identifiers. After authorized indexers are configured and
the observe-only counts look correct, use **Turn automatic adding on** in the
paired setup page. The choice is stored in SQLite and survives restart;
`BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED` only seeds a database that has no
saved choice yet. This opt-in permits the same TorBox download behavior as the
setup page's **Prepare through TorBox** action, so use it only with authorized
sources. Existing Watchlist items remain observation-only, and only items first
seen on a later enabled sync can become eligible. Movies request the exact title
and year. Shows remain observation-only unless **Start new shows with S01E01**
is also enabled; that policy starts a newly observed show at season 1 episode 1.
After Plex reports at least 120 seconds and 10 percent of that exact published
episode played, BlackPearl resolves the next coordinate from bounded Plex
metadata and atomically queues exactly that one episode. It cannot skip directly
to a season or series request. Removing the show from Watchlist or turning the
policy off stops future advancement; work already preparing and published media
remain unchanged. Both controls are durable and non-retroactive, and Watchlist
membership without matching playback never advances the frontier.

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

## Release verification

The repository pins Go 1.26.6 for local, container, and kernel-FUSE builds. Run
the complete local release checks before publishing a candidate:

```bash
make verify
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
cd web && bun install --frozen-lockfile && bun run lint && bun run test && bun run build && bun audit --production
```

The GitHub Actions workflow additionally validates every Compose profile, runs
the privileged Linux FUSE smoke test, and builds both Linux AMD64 and ARM64
images. Local Buildx success is recorded separately from hosted CI, and neither
is presented as Windows Docker Desktop or native-Linux Plex acceptance.

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
