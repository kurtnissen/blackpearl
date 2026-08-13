# Rolling Range Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Direct Play and seek a remote logical MP4 through PearlNFS while BlackPearl stores no complete copy and enforces a 1 MiB rolling cache quota.

**Architecture:** Keep `domain.ReadHandle`, PearlFS, PearlNFS, and `core.Catalog` as the stable read path. Add an HTTP Range gateway at the external boundary and a fixed-chunk `cache.RollingSource` behind `core.MediaSource`; wire it only when storage mode is `rolling`. A separate range-origin container owns the complete legal fixture while the fixture-free BlackPearl image retains at most four 256 KiB chunks.

**Tech Stack:** Go 1.24, standard `net/http`, SQLite catalog, existing PearlNFS adapter, Docker Compose, nginx range origin, `go test` with `testify`, race detector.

## Global Constraints

- Preserve the existing persistent cache, FUSE implementation, Plex container, and production-path safety rules.
- The BlackPearl rolling container must contain no MP4 and receive no source-media mount.
- All logical reads remain `ReadAt(context.Context, []byte, int64)` operations with a stable logical size.
- HTTP origins must return exact HTTP 206 ranges; HTTP 200 fallback is rejected.
- `current chunk bytes + reserved temporary bytes` must never exceed `BLACKPEARL_CACHE_MAX_BYTES`.
- The POC uses a 1,048,576-byte quota and 262,144-byte chunks.
- Filesystems remain read-only; no custom Plex client is introduced.
- All service and domain behavior is implemented test-first and all I/O accepts `context.Context` first.
- Existing Plex, media, download, and `*arr` paths are never searched, mounted, or modified.

---

### Task 1: Register remote POC metadata without importing bytes

**Files:**
- Modify: `internal/core/catalog.go`
- Modify: `internal/core/catalog_test.go`

**Interfaces:**
- Consumes: `domain.BackingRef`, logical object size, existing `Repository.Upsert`.
- Produces: `func (c *Catalog) RegisterPOC(ctx context.Context, backing domain.BackingRef, size int64) (domain.Media, error)`.

- [ ] **Step 1: Write failing service tests**

Add tests proving `RegisterPOC` creates the canonical `BlackPearl POC (2026)` movie with the supplied remote backing and size, upserts it once, rejects an invalid backing reference, rejects a non-positive size, and never calls the persistent importer.

```go
func TestCatalog_RegisterPOC_PersistsRemoteLogicalMedia(t *testing.T) {
    repository := &fakeRepository{}
    importer := &fakeImporter{err: errors.New("must not be called")}
    catalog := NewCatalog(repository, importer, &fakeSource{})
    backing, err := domain.NewBackingRef("http-range", "blackpearl-poc.mp4")
    require.NoError(t, err)

    media, err := catalog.RegisterPOC(context.Background(), backing, 3_417_699)

    require.NoError(t, err)
    assert.Equal(t, backing, media.Backing)
    assert.Equal(t, int64(3_417_699), media.Size)
    assert.Equal(t, "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4", media.VirtualPath)
    assert.Equal(t, 0, importer.calls)
    require.Len(t, repository.upserted, 1)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/core -run 'TestCatalog_RegisterPOC'`

Expected: compilation fails because `RegisterPOC` does not exist.

- [ ] **Step 3: Implement the minimum registration service**

Extract the existing canonical POC construction into a private method used by both `ImportPOC` and `RegisterPOC`. `RegisterPOC` must validate `size > 0`, reconstruct the backing through `domain.NewBackingRef`, build the movie through `domain.NewMovie`, and wrap repository errors with `register POC media` context.

```go
func (c *Catalog) RegisterPOC(ctx context.Context, backing domain.BackingRef, size int64) (domain.Media, error) {
    if size <= 0 {
        return domain.Media{}, fmt.Errorf("POC logical size must be positive: %d", size)
    }
    validated, err := domain.NewBackingRef(backing.Provider, backing.ObjectID)
    if err != nil {
        return domain.Media{}, fmt.Errorf("validate POC backing: %w", err)
    }
    return c.persistPOC(ctx, validated, size)
}
```

- [ ] **Step 4: Run core tests and verify GREEN**

Run: `go test -race ./internal/core`

Expected: all core tests pass under the race detector.

- [ ] **Step 5: Commit the service change**

```bash
git add internal/core/catalog.go internal/core/catalog_test.go
git commit -m "feat: register remote POC media"
```

---

### Task 2: Validate rolling-origin configuration

**Files:**
- Modify: `.env.example`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: environment variables parsed by `caarlos0/env`.
- Produces: `Config.CacheChunkBytes int64`, `Config.RangeOriginURL string`, `Config.RangeObjectID string`, and `Config.RangeTimeout time.Duration`.

- [ ] **Step 1: Write failing table-driven configuration tests**

Cover a valid rolling configuration and failures for a missing origin URL, missing object ID, non-HTTP origin, chunk size larger than quota, non-positive chunk size, non-positive timeout, and simultaneous `BLACKPEARL_POC_SOURCE` plus rolling origin.

```go
func TestParse_AcceptsCompleteRollingRangeConfiguration(t *testing.T) {
    cfg, err := Parse(map[string]string{
        "BLACKPEARL_STORAGE_MODE":      "rolling",
        "BLACKPEARL_CACHE_MAX_BYTES":   "1048576",
        "BLACKPEARL_CACHE_CHUNK_BYTES": "262144",
        "BLACKPEARL_RANGE_ORIGIN_URL":  "http://range-origin/media/",
        "BLACKPEARL_RANGE_OBJECT_ID":   "blackpearl-poc.mp4",
        "BLACKPEARL_RANGE_TIMEOUT":     "30s",
    })

    require.NoError(t, err)
    assert.Equal(t, int64(262144), cfg.CacheChunkBytes)
    assert.Equal(t, 30*time.Second, cfg.RangeTimeout)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/config -run 'TestParse_.*Rolling'`

Expected: compilation fails because the new fields do not exist.

- [ ] **Step 3: Add typed fields and mode-specific validation**

Add:

```go
CacheChunkBytes int64         `env:"BLACKPEARL_CACHE_CHUNK_BYTES" envDefault:"262144"`
RangeOriginURL  string        `env:"BLACKPEARL_RANGE_ORIGIN_URL"`
RangeObjectID   string        `env:"BLACKPEARL_RANGE_OBJECT_ID"`
RangeTimeout    time.Duration `env:"BLACKPEARL_RANGE_TIMEOUT" envDefault:"30s"`
```

Rolling validation requires a positive quota, positive chunk size no larger than quota, positive timeout, empty POC source, non-empty object ID without NUL, and an absolute HTTP(S) origin URL. Persistent mode preserves current behavior and rejects partially supplied rolling-origin settings.

- [ ] **Step 4: Document the variables and verify GREEN**

Update `.env.example` with commented rolling-mode values, then run:

`go test -race ./internal/config`

Expected: all configuration tests pass.

- [ ] **Step 5: Commit configuration**

```bash
git add .env.example internal/config/config.go internal/config/config_test.go
git commit -m "feat: configure rolling range origin"
```

---

### Task 3: Implement the strict HTTP Range gateway

**Files:**
- Create: `internal/gateway/httporigin/gateway.go`
- Create: `internal/gateway/httporigin/gateway_test.go`

**Interfaces:**
- Consumes: `domain.BackingRef`, injected `*http.Client`, configured base URL.
- Produces: `New(baseURL string, client *http.Client) (*Gateway, error)`, `Gateway.Ready(ctx) error`, and `Gateway.Open(ctx, backing) (acquisition.RangeSource, error)`.

- [ ] **Step 1: Write failing gateway tests with `httptest.Server`**

Add table-driven tests for constructor validation and source reads. The working server must require `Range: bytes=4-7`, return `206`, `Content-Range: bytes 4-7/16`, a four-byte body, `Content-Length: 4`, and stable `ETag: "fixture-v1"`.

```go
func TestSource_ReadAt_ReturnsExactInteriorRange(t *testing.T) {
    origin := newRangeServer(t, []byte("0123456789abcdef"))
    gateway, err := New(origin.URL+"/media/", origin.Client())
    require.NoError(t, err)
    backing, err := domain.NewBackingRef("http-range", "movie.mp4")
    require.NoError(t, err)
    source, err := gateway.Open(context.Background(), backing)
    require.NoError(t, err)
    t.Cleanup(func() { require.NoError(t, source.Close()) })

    buffer := make([]byte, 4)
    count, err := source.ReadAt(context.Background(), buffer, 4)

    require.NoError(t, err)
    assert.Equal(t, 4, count)
    assert.Equal(t, "4567", string(buffer))
}
```

Also prove rejection of provider names other than `http-range`, object IDs that alter path structure, HTTP 200 responses, malformed or mismatched `Content-Range`, short and oversized bodies, validator changes, negative offsets, EOF, and cancellation.

- [ ] **Step 2: Run gateway tests and verify RED**

Run: `go test ./internal/gateway/httporigin`

Expected: package or exported constructors are missing.

- [ ] **Step 3: Implement gateway metadata and strict range reads**

Use `url.JoinPath` only after rejecting `/`, `\\`, NUL, `.` and `..` in the object ID. `Open` sends `HEAD`, requires a positive `Content-Length`, and records validators. `ReadAt` computes an overflow-safe inclusive end, sends a GET range, requires 206 and exact `Content-Range`, reads through `io.LimitReader(expected+1)`, rejects extra or missing bytes, and returns standard `io.ReaderAt` EOF behavior.

The source is immutable after `Open`; `Close` marks it closed under a mutex or atomic flag and later reads return a stable closed-source error.

- [ ] **Step 4: Run gateway tests with race detection and verify GREEN**

Run: `go test -race ./internal/gateway/httporigin`

Expected: all response-validation and concurrency tests pass.

- [ ] **Step 5: Commit the gateway**

```bash
git add internal/gateway/httporigin
git commit -m "feat: add strict HTTP range gateway"
```

---

### Task 4: Implement exact rolling chunk reads and quota eviction

**Files:**
- Create: `internal/cache/rolling.go`
- Create: `internal/cache/rolling_test.go`

**Interfaces:**
- Consumes: `RangeOpener.Open`, `acquisition.RangeSource`, `domain.Media`, absolute cache root, byte quota, chunk size, lifecycle context, fetch timeout.
- Produces: `NewRolling(ctx context.Context, options RollingOptions, opener RangeOpener) (*RollingSource, error)`, `RollingSource.Open`, `RollingSource.Ready`, and `RollingSource.Stats`.

```go
type RangeOpener interface {
    Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error)
    Ready(ctx context.Context) error
}

type RollingOptions struct {
    Root         string
    MaxBytes     int64
    ChunkBytes   int64
    FetchTimeout time.Duration
}
```

- [ ] **Step 1: Write failing exact-read and quota tests**

Use a deterministic fake opener that stores source bytes only in memory and records requested offsets. Prove interior, cross-chunk, final partial, EOF, and non-sequential reads. Use a 12-byte logical object with four-byte chunks and an eight-byte quota to prove the third distinct chunk evicts the least-recently-used unpinned chunk.

```go
func TestRollingSource_ReadAt_EvictsWithinHardQuota(t *testing.T) {
    opener := newFakeRangeOpener([]byte("abcdefghijkl"))
    source := newRollingForTest(t, opener, 8, 4)
    handle := openRollingHandle(t, source, 12)

    readExact(t, handle, 0, 4)
    readExact(t, handle, 4, 4)
    readExact(t, handle, 8, 4)
    stats := source.Stats()

    assert.LessOrEqual(t, stats.CurrentBytes+stats.ReservedBytes, int64(8))
    assert.LessOrEqual(t, stats.HighWaterBytes, int64(8))
    assert.Equal(t, uint64(1), stats.Evictions)
}
```

Also assert there is no logical-size file, every published file is no larger than one chunk, object directories use a SHA-256 key, and revisiting offset zero after eviction causes another provider read.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/cache -run 'TestRollingSource'`

Expected: `NewRolling`, `RollingOptions`, or `Stats` is undefined.

- [ ] **Step 3: Implement fixed-chunk storage and hard accounting**

Create `RollingSource` state guarded by one mutex:

```go
type RollingSource struct {
    lifecycle context.Context
    options   RollingOptions
    opener    RangeOpener
    mu        sync.Mutex
    chunks    map[chunkKey]*chunkEntry
    inflight  map[chunkKey]*fetchCall
    current   int64
    reserved  int64
    highWater int64
    notify    chan struct{}
    stats     Stats
}
```

`ReadAt` processes one chunk at a time. A hit pins under the mutex, reads the immutable chunk with `os.File.ReadAt`, then unpins and signals capacity. A miss evicts unpinned LRU entries before reserving the exact expected length. Fetch into a same-directory temporary file, verify length, sync, close, rename, chmod `0640`, sync the directory, then atomically convert reservation to current bytes and pin the entry.

- [ ] **Step 4: Verify GREEN for exact reads and quota**

Run: `go test -race ./internal/cache -run 'TestRollingSource'`

Expected: exact-read, layout, eviction, re-fetch, and hard-quota tests pass.

- [ ] **Step 5: Commit the first rolling cache slice**

```bash
git add internal/cache/rolling.go internal/cache/rolling_test.go
git commit -m "feat: add quota-bound rolling cache"
```

---

### Task 5: Add miss coalescing, backpressure, and restart recovery

**Files:**
- Modify: `internal/cache/rolling.go`
- Modify: `internal/cache/rolling_test.go`

**Interfaces:**
- Consumes: Task 4 `RollingSource`, keyed chunk state, lifecycle context.
- Produces: concurrency-safe one-fetch-per-chunk behavior and deterministic startup recovery.

- [ ] **Step 1: Write failing concurrent and recovery tests**

Use blocking fake sources and channels rather than sleeps. Prove 20 concurrent readers of the same missing chunk cause one origin fetch; a cancelled waiter exits without cancelling the shared fetch; a different-chunk fetch waits while all capacity is pinned; releasing a pin wakes it; failed fetches release reservations and remove temporary files; startup removes `.fetch-*`; and startup evicts oldest chunks when preexisting valid files exceed quota.

```go
func TestRollingSource_ReadAt_CoalescesConcurrentChunkMisses(t *testing.T) {
    opener := newBlockingRangeOpener([]byte("abcdefgh"))
    source := newRollingForTest(t, opener, 8, 4)
    handles := openManyRollingHandles(t, source, 8, 20)

    results := readConcurrently(handles, 0, 4)
    opener.releaseFetch()

    requireAllExact(t, results, "abcd")
    assert.Equal(t, 1, opener.fetchCount(0, 4))
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test -race ./internal/cache -run 'TestRollingSource_.*(Concurrent|Capacity|Restart|Failure)'`

Expected: duplicate fetches occur or recovery/backpressure assertions fail.

- [ ] **Step 3: Implement keyed fetch calls and capacity notifications**

Each `fetchCall` owns a completion channel and result. Shared fetch work derives from the lifecycle context with `context.WithTimeout`; callers wait on either their own context or the shared completion. Reservations occur before the goroutine starts. Closing and replacing the `notify` channel broadcasts state changes without a condition-variable wait that ignores context.

At construction, walk only `rolling/<64-hex>/<16-digit>.chunk`, remove `.fetch-*`, stat valid files, reject oversized chunks, rebuild LRU from modification time, and evict oldest entries until current bytes fit. Do not follow symlinks or scan outside the configured root.

- [ ] **Step 4: Run all cache tests and verify GREEN**

Run: `go test -race ./internal/cache`

Expected: persistent store tests and all rolling concurrency/recovery tests pass without races.

- [ ] **Step 5: Commit concurrency and recovery**

```bash
git add internal/cache/rolling.go internal/cache/rolling_test.go
git commit -m "feat: coalesce rolling cache misses"
```

---

### Task 6: Wire rolling mode into the BlackPearl binary

**Files:**
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Consumes: Task 1 `Catalog.RegisterPOC`, Task 2 rolling config, Task 3 HTTP gateway, Tasks 4-5 rolling source.
- Produces: a single binary that selects persistent or rolling storage without changing filesystem adapters.

- [ ] **Step 1: Write failing application wiring tests**

Replace direct constructors behind narrow dependency functions so tests can inject a fake persistent store, range gateway, and rolling source. Prove rolling mode no longer returns `ErrNotConfigured`, registers remote metadata using the opened logical size, starts NFS with the rolling catalog, and does not call the persistent importer. Retain the test that rolling mode without complete configuration fails before creating paths.

```go
func TestRun_RollingModeRegistersRemotePOCAndStartsNFS(t *testing.T) {
    cfg := validRollingConfig(t)
    deps := fakeRollingDependencies(t, 3_417_699)

    err := run(cancelledAfterReadyContext(t), cfg, testLogger(), deps)

    require.NoError(t, err)
    assert.Equal(t, 1, deps.rangeOpenCalls())
    assert.Equal(t, 0, deps.importCalls())
    assert.Equal(t, 1, deps.nfsStarts())
}
```

- [ ] **Step 2: Run focused app tests and verify RED**

Run: `go test ./cmd/blackpearl -run 'TestRun_Rolling'`

Expected: the existing not-configured branch fails the new expectation.

- [ ] **Step 3: Implement mode-specific dependency wiring**

Open SQLite once. Persistent mode constructs the existing `cache.Store`, builds the catalog, and optionally imports `POCSource`. Rolling mode clones the injected instrumented HTTP client with `Timeout = cfg.RangeTimeout`, constructs `httporigin.Gateway`, constructs `cache.RollingSource`, opens the configured backing once to obtain and validate its size, closes it, builds the catalog, and calls `RegisterPOC`.

Both branches assign a `core.MediaSource` and then execute the unchanged FUSE/NFS, diagnostics, Plex refresh, and shutdown flow. Remove only the rolling `ErrNotConfigured` early return.

- [ ] **Step 4: Run application and full Go tests**

Run: `go test -race ./cmd/blackpearl ./internal/...`

Expected: all packages pass and persistent behavior remains unchanged.

- [ ] **Step 5: Commit binary wiring**

```bash
git add cmd/blackpearl/app.go cmd/blackpearl/app_test.go
git commit -m "feat: run BlackPearl in rolling mode"
```

---

### Task 7: Build the isolated rolling Docker/Plex acceptance stack

**Files:**
- Modify: `Dockerfile`
- Create: `deploy/range-origin.conf`
- Create: `compose.rolling.yaml`
- Create: `scripts/test-rolling-compose.sh`
- Create: `scripts/setup-rolling-poc.sh`
- Create: `scripts/verify-rolling-poc.sh`
- Create: `scripts/cleanup-rolling-poc.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: fixture builder, fixture-free `runtime` target, rolling environment variables, PearlNFS port.
- Produces: isolated `blackpearl-rolling` Compose project on host ports 8081, 20491, and 32401.

- [ ] **Step 1: Write the failing rendered-Compose safety test**

`scripts/test-rolling-compose.sh` must render JSON and fail unless the origin has no published port, BlackPearl uses the fixture-free target, BlackPearl has no bind/source mount, only project-scoped named volumes exist, the Plex NFS volume is read-only, all published ports bind to `127.0.0.1`, rolling quota is 1,048,576, and chunk size is 262,144.

Run: `./scripts/test-rolling-compose.sh`

Expected: failure because `compose.rolling.yaml` does not exist.

- [ ] **Step 2: Add the range-origin image and Compose profile**

Add a Dockerfile `range-origin` target based on pinned nginx Alpine, copying only `/fixture/blackpearl-poc.mp4` into `/srv/media`. `deploy/range-origin.conf` serves `/media/` read-only with automatic ranges, a 1 MiB/s limit, no directory listing, and access logs enabled for re-fetch evidence.

`compose.rolling.yaml` uses project name `blackpearl-rolling`, builds BlackPearl target `runtime`, sets rolling configuration, and publishes host loopback ports 8081, 20491, and 32401. The Plex volume uses Docker's local NFS driver against host port 20491. No service mounts a host media path.

- [ ] **Step 3: Add setup, verification, and scoped cleanup scripts**

`setup-rolling-poc.sh` recreates the three services, obtains the isolated Plex token when claimed, creates only `BlackPearl Rolling POC` rooted at `/blackpearl/Movies`, refreshes it, and waits for the title.

`verify-rolling-poc.sh` must assert:

```text
rolling_blackpearl_ready=PASS
rolling_source_isolated=PASS
rolling_logical_size_exceeds_quota=PASS
rolling_plex_catalog_scan=PASS
rolling_nonsequential_ranges=PASS
rolling_cache_quota=PASS
rolling_evicted_range_refetch=PASS
```

It inspects mounts and the BlackPearl filesystem for MP4 files, compares origin and Plex hashes for beginning/middle/end ranges, streams the full original part while sampling `.chunk` plus `.fetch-*` bytes, verifies the maximum sample is at most 1 MiB, revisits an early evicted range, and confirms nginx logged a new matching range request.

`cleanup-rolling-poc.sh` targets only the exact rolling Compose project; `--remove-data` additionally removes only its named volumes.

- [ ] **Step 4: Verify Compose safety and automated rolling acceptance**

Run:

```bash
make rolling-compose-check
./scripts/setup-rolling-poc.sh
./scripts/verify-rolling-poc.sh
```

Expected: all seven rolling acceptance lines report `PASS`.

- [ ] **Step 5: Commit the rolling stack**

```bash
git add Dockerfile deploy/range-origin.conf compose.rolling.yaml scripts/test-rolling-compose.sh scripts/setup-rolling-poc.sh scripts/verify-rolling-poc.sh scripts/cleanup-rolling-poc.sh Makefile
git commit -m "feat: add rolling Plex acceptance stack"
```

---

### Task 8: Record acceptance and perform the full verification gate

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Create: `docs/macos-rolling-poc.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Consumes: automated evidence from Task 7 and manual Plex Web evidence.
- Produces: reproducible operator instructions and accurately separated evidence states.

- [ ] **Step 1: Document operation and evidence boundaries**

Document startup at `http://localhost:32401/web`, the isolated library, the one-minute-or-less legal test clip, full playback, forward/backward seeks, and the Plex Dashboard Direct Play decision. State separately whether automated rolling checks, macOS client playback, Windows Docker Desktop, and native Linux have been verified.

- [ ] **Step 2: Run the complete static, unit, integration, and safety gate**

Run:

```bash
gofmt -w cmd internal
go vet ./...
go test -race -coverprofile=coverage.out ./...
./scripts/check-coverage.sh coverage.out
./scripts/test-compose-paths.sh
./scripts/test-portable-compose.sh
./scripts/test-rolling-compose.sh
./scripts/verify-portable-poc.sh
./scripts/verify-rolling-poc.sh
git diff --check
```

Expected: zero test failures, zero race reports, at least 80% coverage, all Compose safety checks pass, and both persistent and rolling automated acceptance pass.

- [ ] **Step 3: Inspect the final diff and commit documentation**

Confirm `git status --short` contains only the four scoped documentation files, then commit:

```bash
git add README.md docs/architecture.md docs/macos-rolling-poc.md docs/acceptance-evidence.md
git commit -m "docs: add rolling cache operations and evidence"
```

- [ ] **Step 4: Run the manual Plex gate**

Open the isolated Plex server, play the full logical movie, seek forward and backward, and inspect Plex logs or Dashboard for `Direct play OK`. Record the client, decision, seek result, commit SHA, and date in `docs/acceptance-evidence.md`; do not mark Windows or native Linux verified.

- [ ] **Step 5: Re-run affected checks after recording manual evidence**

Run: `./scripts/verify-rolling-poc.sh && git diff --check && git status --short`

Expected: automated acceptance stays green and only the intentional evidence update remains before its final documentation commit.
