# BlackPearl Milestone 1 FUSE/Plex Design

## Purpose

BlackPearl is a Docker-deployable Go service that will eventually resolve and retrieve media from authorized acquisition providers while presenting a stable virtual library to Plex. Milestone 1 proves only the foundational boundary: Plex can scan and play a legally usable video exposed through a BlackPearl-owned FUSE mount.

The proof must not read, mount, modify, or scan an existing Plex, *arr, download, or media path. All state and test data are created inside the repository's ignored `runtime/` tree on the target Ubuntu Server.

## Milestone 1 outcome

On Ubuntu Server, an operator can:

1. prepare an isolated shared mountpoint with the supplied script;
2. generate a synthetic test-pattern MP4 with the supplied Docker fixture image;
3. start BlackPearl and the opt-in Plex acceptance container;
4. observe the movie path through the FUSE mount;
5. add `/blackpearl/Movies` as an isolated Plex library;
6. scan and play `BlackPearl POC (2026)`; and
7. stop the stack and clean only the repository-owned runtime directory.

Automated tests prove byte-for-byte reads through the filesystem implementation. The manual Plex acceptance proves compatibility with Plex's scanner and playback behavior. These are separate evidence gates.

## Scope

### Included

- One Go 1.24+ service and one process.
- FUSE filesystem mounted by BlackPearl using `go-fuse/v2`.
- SQLite catalog and schema migrations owned by BlackPearl.
- A local cache directory owned by BlackPearl.
- Startup import of one generated, legally unrestricted test-pattern MP4.
- Movie-style virtual paths suitable for Plex scanning.
- Health and readiness HTTP endpoints.
- A narrow Plex refresh gateway, disabled unless a base URL, token, and library section are configured.
- Generic acquisition-provider and resolver contracts with no network provider implementation.
- Docker image, production-oriented Compose file, opt-in Plex POC override, and Ubuntu setup/verification scripts.
- Unit, integration, FUSE smoke, build, and documentation checks.

### Explicitly excluded

- Prowlarr, Usenet, TorBox, torrents, debrid, indexer, or downloader integration.
- Progressive or ranged remote retrieval.
- RAR decoding, repair, unpacking, or parity handling.
- Read-ahead, next-episode prefetch, cache eviction, or quota enforcement.
- Modification of an existing Plex server or library.
- Sonarr, Radarr, or other *arr integration.
- Automatic claiming or account setup for Plex.
- Production deployment or migration of real media.

## Architectural approach

BlackPearl is a modular monolith. Package boundaries represent future replaceable adapters without introducing more services or containers.

```text
Plex scanner/player
        |
        v
PearlFS (FUSE adapter)
        |
        v
Core catalog service
   |           |
   v           v
SQLite state   Cache reader
                   |
                   v
             BlackPearl-owned file

Core service --> Resolver contract --> Acquisition Provider contract
Core service --> Plex gateway (optional refresh request)
```

The service follows the required dependency direction:

```text
FUSE/HTTP adapters -> core service -> repository/gateway interfaces
                                  -> SQLite/cache/Plex implementations
```

The core package does not import FUSE, HTTP-server, SQLite-driver, or Plex HTTP details. Interfaces are defined at the consuming service boundary. The acquisition package contains domain request/result types; the resolver service owns the narrow provider interface it consumes.

## Package responsibilities

### `cmd/blackpearl`

Wires typed configuration, structured logging, SQLite, cache, core service, optional Plex gateway, HTTP diagnostics, and PearlFS. It contains no business logic.

### `internal/domain`

Defines zero-infrastructure-dependency media identifiers, catalog entries, virtual paths, byte ranges, and typed sentinel errors.

### `internal/core`

Owns catalog orchestration and the repository/cache interfaces it consumes. At startup it imports the configured POC fixture into the cache if absent, persists the catalog entry, and exposes immutable lookup/list/open operations to PearlFS.

### `internal/state`

Implements catalog persistence with SQLite. Migrations are embedded and applied transactionally at startup. The initial schema stores media ID, title, year, virtual path, media type, size, cache key, and timestamps.

### `internal/cache`

Owns cache paths and safe local-file reads. Imports use a temporary file, content hashing, `fsync`, and atomic rename. Callers use opaque cache keys rather than arbitrary paths. The FUSE adapter never opens operator-supplied filesystem paths directly.

### `internal/pearlfs`

Maps the catalog into a read-only hierarchy and implements the minimum operations Plex requires: root and directory lookup/readdir, file lookup/getattr/open, and offset-based reads. It exposes:

```text
/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4
```

Mutation operations are unsupported. File handles are read-only and allow random access so Plex can probe media headers and seek.

### `internal/plex`

Implements a small HTTP gateway for a configured Plex library refresh. It sends the Plex token as a header, never logs it, uses bounded timeouts, and is disabled by default. Plex library creation and account claiming remain manual acceptance steps.

### `internal/acquisition`

Defines generic provider-neutral request, candidate, object metadata, and ranged-reader concepts. It does not name or implement any future provider.

### `internal/resolver`

Defines the resolver service and the narrow acquisition provider interface it consumes. Milestone 1 returns a typed `not configured` result. Later milestones can add selection policy without changing PearlFS.

### `internal/httpserver`

Exposes `/healthz` for process liveness and `/readyz` for SQLite/cache/catalog/FUSE readiness. It contains only HTTP translation and calls a readiness service.

## Startup and data flow

1. Configuration is loaded from environment variables and validated before side effects.
2. BlackPearl creates only its configured data, cache, and mount directories.
3. SQLite opens with foreign keys, busy timeout, and WAL mode; embedded migrations run.
4. If `BLACKPEARL_POC_SOURCE` is configured, core imports that file into the cache and upserts the single POC catalog record.
5. PearlFS mounts the catalog at `BLACKPEARL_MOUNT_PATH` in read-only mode.
6. The readiness endpoint becomes successful only after the database, cached POC object, and FUSE mount are ready.
7. Plex scans the propagated mount from its own read-only bind.
8. Plex metadata probes and playback reads reach PearlFS, which resolves the catalog entry and performs offset reads against the cached object.

Shutdown first marks the service unready, then stops HTTP, unmounts FUSE, closes open resources, and closes SQLite.

## Configuration

All paths default inside the container and must be absolute:

| Variable | Default | Purpose |
|---|---|---|
| `BLACKPEARL_DATA_DIR` | `/var/lib/blackpearl` | Service-owned persistent state |
| `BLACKPEARL_DB_PATH` | `/var/lib/blackpearl/blackpearl.db` | SQLite database |
| `BLACKPEARL_CACHE_DIR` | `/var/lib/blackpearl/cache` | Cached objects |
| `BLACKPEARL_MOUNT_PATH` | `/mnt/blackpearl` | FUSE mountpoint |
| `BLACKPEARL_POC_SOURCE` | empty | Optional generated fixture path |
| `BLACKPEARL_HTTP_ADDR` | `:8080` | Diagnostics listener |
| `BLACKPEARL_LOG_LEVEL` | `info` | Structured log level |
| `BLACKPEARL_PLEX_URL` | empty | Optional Plex server base URL |
| `BLACKPEARL_PLEX_TOKEN` | empty | Optional Plex server token |
| `BLACKPEARL_PLEX_SECTION_ID` | empty | Optional library section to refresh |

The three Plex values are all-or-none. Secrets are supplied through the environment for the POC and are never persisted by BlackPearl.

## Container and host model

The normal Compose file runs BlackPearl only. The POC override adds a one-shot fixture generator and an isolated official Plex container. BlackPearl alone receives `/dev/fuse`, `SYS_ADMIN`, and the minimum AppArmor exception required for FUSE on Ubuntu.

Linux mount namespaces do not propagate a FUSE mount to a sibling container by default. The Ubuntu setup script therefore creates a repository-owned bind mount and marks it shared. BlackPearl receives it as `rshared`; Plex receives the same path as read-only `rslave`. Docker documents bind propagation as a Linux-host feature, so Plex cross-container acceptance is not claimed on Docker Desktop for macOS.

No Compose file references `/media`, an existing Plex config, or an *arr directory. The only host bind roots are explicit paths under `runtime/`.

## Error handling and safety

- Invalid configuration fails before mounting.
- A missing or invalid POC fixture fails startup with contextual errors.
- Cache imports never expose a partial object.
- SQLite errors are wrapped with operation context and mapped to domain sentinel errors where applicable.
- FUSE maps not-found and invalid-range conditions to stable errno values.
- The filesystem is read-only; unlink, rename, create, and write are rejected.
- Plex refresh failures are reported but cannot corrupt the catalog or cache.
- Logs are JSON in production, contain request IDs at HTTP boundaries, and redact Plex credentials.
- The cleanup script refuses paths outside the repository-owned runtime root and unmounts before removing data.

## Testing strategy

### Unit

- Domain virtual-path validation and deterministic catalog mapping.
- Cache import, hashing, atomic replacement, full reads, partial reads, and bounds.
- Core import/upsert and catalog lookup with fakes at repository/cache boundaries.
- Resolver behavior with provider fakes.
- Plex refresh request method/path/header and error mapping with `httptest`.
- Configuration all-or-none and absolute-path validation.

### Integration

- SQLite migration, upsert, lookup, and reopen persistence against a temporary database.
- HTTP liveness/readiness behavior with real readiness state.
- PearlFS node operations against a temporary cache without requiring a kernel mount.

### Linux FUSE smoke

An opt-in test mounts PearlFS in a temporary directory, compares the exposed MP4 to the cached fixture byte-for-byte, performs nonsequential range reads, and cleanly unmounts. It runs inside the privileged BlackPearl container and on Ubuntu CI runners with `/dev/fuse`.

### Plex acceptance

The Ubuntu guide records a manual evidence checklist: Plex container healthy, virtual movie visible from the Plex container, library scan finds exactly the POC title, playback starts, seeking succeeds, and BlackPearl logs show reads. This is not replaced by unit tests or a successful container build.

## Acceptance criteria

Milestone 1 is accepted only when all of the following are true:

1. `go test -race -cover ./...` succeeds with at least 80% line coverage for handwritten packages.
2. `go vet ./...` and the configured linter succeed.
3. `docker build` succeeds for both `linux/amd64` and `linux/arm64`, or the unavailable architecture is clearly reported rather than inferred.
4. Compose configuration renders successfully with no production host paths.
5. The Linux FUSE smoke test proves exact bytes and offset reads through the mounted virtual file.
6. On Ubuntu Server, the Plex container sees the propagated virtual path read-only.
7. Plex scans `BlackPearl POC (2026)` from the isolated library.
8. Plex starts playback and can seek in the synthetic MP4.
9. Stopping the stack cleanly unmounts PearlFS.
10. Cleanup affects only the BlackPearl repository's `runtime/` directory.

Criteria 6-8 require an actual Ubuntu/Plex run. Until that evidence is recorded, the repository may be described as code-complete or locally verified but not as having proven Plex acceptance.

## Roadmap after Milestone 1

1. Provider-neutral resolver and candidate ranking with contract tests.
2. One authorized acquisition provider behind the generic interface.
3. Sparse/read-through cache with range integrity and backpressure.
4. Progressive retrieval and seek-aware scheduling.
5. Next-episode prefetch policy with bounded concurrency.
6. Quota-based eviction with pinning and active-reader protection.
7. Optional Prowlarr discovery gateway and additional authorized providers.

Each roadmap item requires its own design and acceptance evidence. No later milestone is required for the FUSE/Plex POC.
