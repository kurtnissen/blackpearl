# BlackPearl Milestone 1 FUSE/Plex Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Docker-deployable Go service that exposes a BlackPearl-owned synthetic MP4 through read-only FUSE so an isolated Plex server on Ubuntu can scan, play, and seek it.

**Architecture:** A modular Go monolith wires PearlFS and HTTP adapters to a core catalog service. The core depends on narrow repository, cache, resolver, and Plex gateway contracts; SQLite, a content-addressed local cache, FUSE, and HTTP clients implement those boundaries. Acquisition remains provider-neutral and inert in Milestone 1.

**Tech Stack:** Go 1.24, `go-fuse/v2` v2.11.0, `modernc.org/sqlite` v1.40.1, `caarlos0/env/v11`, OpenTelemetry, `testify`, Docker/Compose, SQLite, Plex's official Docker image, Vitest-equivalent Go test tooling (`go test`, race detector, coverage).

## Global Constraints

- Target Ubuntu Server; macOS Docker Desktop may verify the image and in-container FUSE but not sibling-container mount propagation.
- Keep all runtime paths under repository-owned `runtime/`; never reference production Plex, *arr, download, or media paths.
- One BlackPearl Go service initially; the opt-in Plex container exists only for acceptance testing.
- Go 1.24+ with explicit error handling, contextual wrapping, `context.Context` first for I/O, no `init`, and no mutable globals.
- Enforce adapter -> service -> repository/gateway dependency direction.
- Write tests first and observe the expected failure before implementation.
- Use structured `slog` output, request IDs, redaction, and OpenTelemetry at HTTP and gateway boundaries.
- Treat Ubuntu Plex scan/play evidence as pending until it is actually run.

---

## File map

```text
cmd/blackpearl/main.go                       dependency wiring and lifecycle
internal/domain/media.go                     infrastructure-free media types
internal/config/config.go                    typed environment configuration
internal/core/catalog.go                     catalog orchestration and interfaces
internal/state/sqlite.go                     SQLite repository
internal/state/migrations/001_catalog.sql    initial schema
internal/cache/store.go                      safe content-addressed file cache
internal/acquisition/types.go                provider-neutral acquisition types
internal/resolver/service.go                 provider consumer and inert resolver
internal/plex/gateway.go                     optional Plex refresh gateway
internal/pearlfs/filesystem.go               read-only FUSE node tree
internal/httpserver/server.go                liveness and readiness routes
internal/platform/telemetry.go               OTel initialization
internal/platform/logger.go                  structured logger construction
Dockerfile                                   multi-stage service and POC image
compose.yaml                                 BlackPearl-only Ubuntu deployment
compose.poc.yaml                             isolated Plex acceptance override
scripts/prepare-ubuntu-poc.sh                safe shared-mount preparation
scripts/verify-fuse.sh                       mounted-byte and seek checks
scripts/cleanup-poc.sh                       guarded unmount and cleanup
docs/ubuntu-plex-poc.md                      manual Plex acceptance runbook
.github/workflows/ci.yaml                    static, unit, build, FUSE smoke gates
```

### Task 1: Module, domain model, and validated configuration

**Files:**
- Create: `go.mod`
- Create: `internal/domain/media.go`
- Create: `internal/domain/media_test.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `.gitignore`

**Interfaces:**
- Produces: `domain.MediaID`, `domain.Media`, `domain.ErrNotFound`, `domain.ErrNotConfigured`.
- Produces: `config.Load() (config.Config, error)` and `config.Parse(environment map[string]string) (config.Config, error)`.

- [ ] **Step 1: Write failing domain and configuration tests**

Test that the canonical POC media produces `Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4`; reject traversal and relative configured storage paths; accept defaults; require the three Plex settings all-or-none.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/domain ./internal/config`

Expected: compilation fails because the packages and exported types do not exist.

- [ ] **Step 3: Add minimal typed implementations**

Use a validated constructor:

```go
func NewMovie(id MediaID, title string, year int, extension string, size int64, cacheKey string) (Media, error)
```

Use `caarlos0/env/v11` for process environment loading and its map-backed environment option for tests. Reject any non-absolute data, database, cache, mount, or configured POC source path.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/domain ./internal/config`

Expected: both packages pass.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .gitignore internal/domain internal/config
git commit -m "feat: add domain and typed configuration"
```

### Task 2: Content-addressed cache and SQLite state

**Files:**
- Create: `internal/cache/store.go`
- Create: `internal/cache/store_test.go`
- Create: `internal/state/sqlite.go`
- Create: `internal/state/sqlite_test.go`
- Create: `internal/state/migrations/001_catalog.sql`

**Interfaces:**
- Produces: `cache.New(root string) (*cache.Store, error)`.
- Produces: `(*cache.Store).Import(ctx context.Context, source string) (key string, size int64, err error)`.
- Produces: `(*cache.Store).Open(ctx context.Context, key string) (cache.Reader, error)` where `Reader` combines `io.ReaderAt`, `io.Closer`, and `Size() int64`.
- Produces: `state.Open(ctx context.Context, path string) (*state.Repository, error)`, `Upsert`, `GetByVirtualPath`, `List`, `Ping`, and `Close`.

- [ ] **Step 1: Write failing cache tests**

Cover SHA-256 keys, duplicate imports, source mutation after import, range reads, rejected malformed keys, and absence of temporary files after success.

- [ ] **Step 2: Verify cache RED**

Run: `go test ./internal/cache`

Expected: compilation fails because `Store` does not exist.

- [ ] **Step 3: Implement the cache minimally**

Copy to a cache-root temporary file while hashing, sync and close it, then atomically rename to `<sha256>.blob`. Validate keys against exactly 64 lowercase hexadecimal characters before opening.

- [ ] **Step 4: Verify cache GREEN**

Run: `go test ./internal/cache`

Expected: all cache behaviors pass.

- [ ] **Step 5: Write failing SQLite tests**

Use `t.TempDir()` to prove migration, upsert idempotency, virtual-path lookup, deterministic list order, not-found mapping, and persistence after close/reopen.

- [ ] **Step 6: Verify SQLite RED**

Run: `go test ./internal/state`

Expected: compilation fails because `state.Open` does not exist.

- [ ] **Step 7: Implement SQLite repository**

Embed and transactionally apply numbered migrations. Open the pure-Go SQLite driver with foreign keys, a five-second busy timeout, and WAL mode. Scan rows through one private function to keep mapping consistent.

- [ ] **Step 8: Verify SQLite GREEN and commit**

Run: `go test ./internal/cache ./internal/state`

```bash
git add internal/cache internal/state go.mod go.sum
git commit -m "feat: add cache and sqlite catalog state"
```

### Task 3: Core orchestration and provider-neutral future boundaries

**Files:**
- Create: `internal/core/catalog.go`
- Create: `internal/core/catalog_test.go`
- Create: `internal/acquisition/types.go`
- Create: `internal/acquisition/types_test.go`
- Create: `internal/resolver/service.go`
- Create: `internal/resolver/service_test.go`

**Interfaces:**
- Consumes: repository `Upsert`, `GetByVirtualPath`, `List`, `Ping`; cache `Import`, `Open`.
- Produces: `core.NewCatalog(repository Repository, cache Cache) *Catalog`.
- Produces: `(*Catalog).ImportPOC(ctx context.Context, source string) (domain.Media, error)`, `List`, `Lookup`, `Open`, and `Ready`.
- Produces: `resolver.Provider.Resolve(ctx context.Context, acquisition.Request) ([]acquisition.Candidate, error)` and `resolver.Service.Resolve`.

- [ ] **Step 1: Write failing core tests with fakes at boundaries**

Prove the POC import metadata, upsert behavior, delegated lookup/open, readiness failure context, and no direct filesystem or SQLite coupling.

- [ ] **Step 2: Verify core RED**

Run: `go test ./internal/core`

Expected: compilation fails because `Catalog` does not exist.

- [ ] **Step 3: Implement the minimum catalog service**

Import the source through `Cache`, construct the canonical POC movie through the domain constructor, and persist it through `Repository`. Wrap every boundary error with its operation.

- [ ] **Step 4: Verify core GREEN**

Run: `go test ./internal/core`

Expected: all core tests pass.

- [ ] **Step 5: Write and implement acquisition/resolver contract tests**

Prove invalid byte ranges are rejected and a resolver with no providers returns `domain.ErrNotConfigured`; prove configured providers receive the request and candidates are returned without provider-specific types leaking.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/core ./internal/acquisition ./internal/resolver`

```bash
git add internal/core internal/acquisition internal/resolver
git commit -m "feat: add catalog and acquisition boundaries"
```

### Task 4: Plex refresh gateway and diagnostics HTTP adapter

**Files:**
- Create: `internal/plex/gateway.go`
- Create: `internal/plex/gateway_test.go`
- Create: `internal/httpserver/server.go`
- Create: `internal/httpserver/server_test.go`

**Interfaces:**
- Produces: `plex.New(baseURL, token, sectionID string, client *http.Client) (*Gateway, error)`.
- Produces: `(*plex.Gateway).Refresh(ctx context.Context) error` using `GET /library/sections/{id}/refresh` and `X-Plex-Token`.
- Produces: `httpserver.New(readiness Readiness, logger *slog.Logger) http.Handler`.

- [ ] **Step 1: Write failing Plex gateway tests**

Use `httptest.Server` to assert the exact method, escaped section path, token header, no token in URL, success on 2xx, bounded response-body handling, and contextual error on non-2xx.

- [ ] **Step 2: Verify Plex RED, implement, and verify GREEN**

Run before implementation and after: `go test ./internal/plex`

Expected before: compile failure. Expected after: pass.

- [ ] **Step 3: Write failing HTTP tests**

Prove `/healthz` is always 200, `/readyz` is 200 only when the readiness dependency succeeds, unsupported methods are 405, and request IDs are returned in `X-Request-Id`.

- [ ] **Step 4: Verify HTTP RED, implement, and verify GREEN**

Run before implementation and after: `go test ./internal/httpserver`

Expected before: compile failure. Expected after: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/plex internal/httpserver
git commit -m "feat: add plex refresh and diagnostics adapters"
```

### Task 5: Read-only PearlFS and kernel-mounted smoke test

**Files:**
- Create: `internal/pearlfs/filesystem.go`
- Create: `internal/pearlfs/filesystem_test.go`
- Create: `internal/pearlfs/mount_linux_test.go`

**Interfaces:**
- Consumes: `Catalog.List(ctx)`, `Catalog.Open(ctx, virtualPath)` returning a sized `io.ReaderAt`/`io.Closer`.
- Produces: `pearlfs.New(catalog Catalog) (*Root, error)`.
- Produces: `pearlfs.Mount(ctx context.Context, mountPath string, root *Root) (*fs.Server, error)`.

- [ ] **Step 1: Write failing filesystem tests**

Test deterministic hierarchy construction, read-only modes, exact full reads, nonsequential reads, EOF behavior, missing files, and rejection of create/write operations through the exposed node capabilities.

- [ ] **Step 2: Verify PearlFS RED**

Run: `go test ./internal/pearlfs`

Expected: compilation fails because the filesystem types do not exist.

- [ ] **Step 3: Implement the minimum go-fuse node tree**

Build `Movies/<title (year)>/<filename>` from catalog records. Implement only lookup/readdir/getattr/open/read/release; return stable errno values and `FOPEN_KEEP_CACHE`. Mount with `fs.NewNodeFS`, read-only options, and a descriptive filesystem name.

- [ ] **Step 4: Verify unit GREEN**

Run: `go test ./internal/pearlfs`

Expected: package tests pass without requiring `/dev/fuse`.

- [ ] **Step 5: Add the opt-in mounted smoke test**

Gate the kernel test with `BLACKPEARL_FUSE_TEST=1`. Mount into `t.TempDir()`, compare the virtual file to a generated temporary source, read from a nonzero offset, unmount, and use `t.Cleanup` for every resource.

- [ ] **Step 6: Run mounted smoke in Linux**

Run: `docker run --rm --privileged -v "$PWD":/src -w /src golang:1.24-bookworm sh -c 'apt-get update -qq && apt-get install -y -qq fuse3 >/dev/null && BLACKPEARL_FUSE_TEST=1 go test -race ./internal/pearlfs -run TestMounted'`

Expected: mounted byte comparison and seek test pass.

- [ ] **Step 7: Commit**

```bash
git add internal/pearlfs go.mod go.sum
git commit -m "feat: expose catalog through read-only pearlfs"
```

### Task 6: Process wiring, telemetry, and graceful lifecycle

**Files:**
- Create: `internal/platform/logger.go`
- Create: `internal/platform/logger_test.go`
- Create: `internal/platform/telemetry.go`
- Create: `internal/platform/telemetry_test.go`
- Create: `cmd/blackpearl/main.go`
- Create: `cmd/blackpearl/app.go`
- Create: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Produces: `platform.NewLogger(level string, writer io.Writer) (*slog.Logger, error)`.
- Produces: `platform.InitTelemetry(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error)`.
- Produces: `run(ctx context.Context, cfg config.Config, stdout, stderr io.Writer) error` for process-level tests.

- [ ] **Step 1: Write failing platform and application tests**

Prove log-level validation, JSON output, cancellation-driven shutdown, startup POC import, HTTP readiness transition, and cleanup after a startup failure. Inject mount and HTTP server factories so tests do not need kernel FUSE.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/platform ./cmd/blackpearl`

Expected: compilation fails because wiring functions do not exist.

- [ ] **Step 3: Implement platform and wiring**

Initialize telemetry once from `main`, build dependencies in order, mount before readiness, use `signal.NotifyContext`, and shut down in reverse order. Wrap the HTTP adapter with OTel HTTP instrumentation and add manual spans around POC import and Plex refresh.

- [ ] **Step 4: Verify GREEN**

Run: `go test -race ./internal/platform ./cmd/blackpearl`

Expected: all lifecycle tests pass under the race detector.

- [ ] **Step 5: Commit**

```bash
git add internal/platform cmd go.mod go.sum
git commit -m "feat: wire blackpearl service lifecycle"
```

### Task 7: Docker, Compose, and safe Ubuntu acceptance tooling

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `compose.yaml`
- Create: `compose.poc.yaml`
- Create: `.env.example`
- Create: `scripts/prepare-ubuntu-poc.sh`
- Create: `scripts/verify-fuse.sh`
- Create: `scripts/cleanup-poc.sh`
- Create: `scripts/test-compose-paths.sh`

**Interfaces:**
- Produces: OCI image targets `runtime` and `poc`.
- Produces: Compose service `blackpearl`; POC services `blackpearl` override and `plex`.
- Produces: scripts that accept no arbitrary delete target and resolve the repository root from their own location.

- [ ] **Step 1: Write the failing Compose safety check**

The script renders both Compose files and rejects host binds outside `${REPOSITORY_ROOT}/runtime`, missing read-only Plex media flags, or FUSE privileges assigned to Plex.

- [ ] **Step 2: Verify safety RED**

Run: `bash scripts/test-compose-paths.sh`

Expected: failure because the Compose files do not exist.

- [ ] **Step 3: Add images and Compose definitions**

Build a static Go binary in `golang:1.24-bookworm`; use a small Debian runtime with `fuse3` and CA certificates. The `poc` target copies an MP4 generated from FFmpeg `testsrc2` plus a sine tone in an isolated builder stage. Pin the official Plex image to the version recorded in Plex's maintained deployment metadata.

- [ ] **Step 4: Add guarded Ubuntu scripts**

The preparation script must require Linux, verify `/dev/fuse`, create `runtime/{data,mount,plex-config,transcode}`, bind the mount directory onto itself, and mark it shared. Cleanup must verify the resolved target begins with the repository runtime root, stop Compose, unmount only `runtime/mount`, and leave deletion as a separate explicit `--remove-data` option.

- [ ] **Step 5: Verify Compose and build**

Run:

```bash
bash scripts/test-compose-paths.sh
docker compose -f compose.yaml -f compose.poc.yaml config --quiet
docker build --target poc -t blackpearl:poc .
```

Expected: safety check, Compose rendering, and image build pass.

- [ ] **Step 6: Run in-container FUSE verification**

Run the POC image with `/dev/fuse` and `SYS_ADMIN`, wait for `/readyz`, compare the mounted file to the baked fixture, issue offset reads, and stop it cleanly.

- [ ] **Step 7: Commit**

```bash
git add Dockerfile .dockerignore compose.yaml compose.poc.yaml .env.example scripts
git commit -m "feat: add safe docker fuse poc"
```

### Task 8: Documentation, CI, and complete verification

**Files:**
- Create: `README.md`
- Create: `docs/architecture.md`
- Create: `docs/ubuntu-plex-poc.md`
- Create: `docs/acceptance-evidence.md`
- Create: `.github/workflows/ci.yaml`
- Create: `Makefile`
- Modify: all files flagged by verification only within Milestone 1 scope.

**Interfaces:**
- Produces: `make check`, `make image`, `make fuse-smoke`, `make compose-check`.
- Produces: an acceptance record that marks every unrun Ubuntu/Plex step as pending.

- [ ] **Step 1: Write operator documentation**

Document prerequisites, threat boundary, directory ownership, architecture, configuration, local developer checks, Ubuntu shared-mount preparation, obtaining a short-lived `PLEX_CLAIM`, isolated library creation, play/seek evidence, shutdown, and guarded cleanup.

- [ ] **Step 2: Add CI**

Run formatting check, `go vet`, race tests with coverage, coverage-floor enforcement, Docker build, Compose safety, and an Ubuntu FUSE smoke job. Do not run Plex or require account secrets in pull requests.

- [ ] **Step 3: Run full verification**

Run:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
bash scripts/test-compose-paths.sh
docker compose -f compose.yaml -f compose.poc.yaml config --quiet
docker build --target poc -t blackpearl:poc .
```

Expected: every command exits zero; measured coverage meets the documented floor or the actual shortfall remains explicitly open.

- [ ] **Step 4: Run local Docker FUSE smoke**

Run: `make fuse-smoke`

Expected: exact bytes and nonsequential reads pass through a kernel FUSE mount inside the Linux container.

- [ ] **Step 5: Reconcile acceptance evidence**

Mark automated criteria with command output and date. Leave Ubuntu sibling-container propagation plus Plex scan/play/seek pending until performed on Ubuntu Server.

- [ ] **Step 6: Commit**

```bash
git add README.md docs .github Makefile
git commit -m "docs: add milestone 1 operations and acceptance"
```

## Final review gate

Re-read the design specification and map every included requirement to one implemented file and one verification result. Inspect `git diff --check`, `git status`, recent commits, and the rendered Compose model. Report local evidence separately from the still-pending Ubuntu Plex acceptance.
