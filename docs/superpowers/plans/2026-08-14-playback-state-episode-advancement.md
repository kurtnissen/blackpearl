# Playback-state episode advancement implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Advance an opted-in Plex Watchlist show from its currently published episode to exactly one metadata-resolved next episode after real playback crosses the durable safety threshold.

**Architecture:** Add bounded local Plex playback and Plex metadata gateways, resolve playback paths against the active BlackPearl manifest, and optimistically advance the existing Watchlist show row as a serialized episode frontier. The existing durable acquisition worker remains the only component that searches providers, prepares content, publishes media, refreshes Plex, and retries failures.

**Tech Stack:** Go 1.24+, standard `net/http`, SQLite with `modernc.org/sqlite`, OpenTelemetry, Docker Compose, Plex HTTP APIs, Testify, Vitest, Brave Plex Web.

## Global Constraints

- Keep BlackPearl one Go service and leave Plex, Prowlarr, and provider containers unmodified.
- Require both automatic Watchlist acquisition and `pilot` show policy; default durable policy remains off.
- Accept only exact BlackPearl episode paths and advance one exact episode, never a season or series.
- Require at least 120 seconds and 10 percent playback progress.
- Preserve range-oriented `ReadAt`, logical files, rolling/persistent storage, Direct Play, seeking, and NFS portability.
- Keep Plex and provider credentials out of URLs, responses, persistence, logs, tests, and browser storage.
- Every HTTP response is bounded, redirects are disabled, errors are sanitized, and I/O accepts `context.Context` first.
- Use TDD for every production change and run all Go tests with `-race`.
- Do not remove published media or alter non-BlackPearl Plex libraries and Watchlist entries.

---

### Task 1: Provider-neutral playback evidence

**Files:**
- Create: `internal/domain/playback.go`
- Create: `internal/domain/playback_test.go`

**Interfaces:**
- Produces: `domain.PlaybackState`, `domain.EpisodeCoordinate`, `domain.EpisodePlayback`, `domain.NewEpisodeCoordinate`, `domain.NewEpisodePlayback`, and `EpisodePlayback.Qualifies`.
- Consumes: standard `time.Duration` and existing safe relative-path rules.

- [x] **Step 1: Write failing domain tests**

Add table-driven tests proving valid playing/paused episode evidence, rejection of movies/unsafe paths/invalid coordinates/negative or overlong timing, and the two-part threshold:

```go
playback, err := domain.NewEpisodePlayback(
    "plex://show/5d9c086ce98e47001eb0f520",
    "TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4",
    1, 1, 130*time.Second, 20*time.Minute, domain.PlaybackStatePaused,
)
require.NoError(t, err)
require.True(t, playback.Qualifies(120*time.Second, 10))
require.False(t, playback.Qualifies(180*time.Second, 10))
```

- [x] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/domain -run 'TestEpisodePlayback|TestEpisodeCoordinate' -count=1`

Expected: build failure because the playback types do not exist.

- [x] **Step 3: Implement immutable domain values**

Implement constructors with private fields and accessors. `Qualifies` must avoid multiplication overflow by comparing bounded durations after constructor validation. Valid percent is 1 through 99; valid timings are positive and no greater than seven days; view offset is clamped only by rejection when it exceeds duration.

- [x] **Step 4: Run focused and package race tests**

Run:

```bash
go test -race ./internal/domain -count=1
```

Expected: PASS.

- [x] **Step 5: Commit the domain contract**

```bash
git add internal/domain/playback.go internal/domain/playback_test.go
git commit -m "feat: model bounded episode playback evidence"
```

### Task 2: Bounded local Plex playback gateway

**Files:**
- Create: `internal/gateway/plexplayback/gateway.go`
- Create: `internal/gateway/plexplayback/gateway_test.go`

**Interfaces:**
- Consumes: consumer-defined `TokenSource { Token(context.Context) (string, error) }` and `domain.NewEpisodePlayback`.
- Produces: `Gateway.Snapshot(ctx context.Context) ([]domain.EpisodePlayback, error)` and `ErrUnavailable`.

- [x] **Step 1: Write failing HTTP contract tests**

Use `httptest.Server` and a fake token source. Cover a real-shaped `/status/sessions` JSON response, header-only authentication, exact `/blackpearl` prefix stripping, playing and paused states, multiple selected parts, movies, foreign roots, malformed session isolation, envelope size/count mismatch, 401/403, redirects, oversized body, cancellation, and sanitized errors.

The success assertion must contain no client/session identifiers:

```go
actual, err := gateway.Snapshot(context.Background())
require.NoError(t, err)
require.Equal(t, "plex://show/5d9c086ce98e47001eb0f520", actual[0].ExternalShowID())
require.Equal(t, "TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4", actual[0].VirtualPath())
```

- [x] **Step 2: Run the gateway test and verify RED**

Run: `go test ./internal/gateway/plexplayback -count=1`

Expected: build failure because the package does not exist.

- [x] **Step 3: Implement the strict gateway**

Use these hard bounds:

```go
const (
    maximumResponseBytes = 2 << 20
    maximumSessions = 64
)
```

Clone the HTTP client and set `CheckRedirect` to return `http.ErrUseLastResponse`. Build the URL with `url.JoinPath(baseURL, "status", "sessions")`; put the token only in `X-Plex-Token`; accept only one selected episode part; and return normalized domain values. Map 401/403 to `domain.ErrUnauthorized` and all other non-context failures to `ErrUnavailable` without response bodies.

- [x] **Step 4: Run gateway and full affected race tests**

Run:

```bash
go test -race ./internal/domain ./internal/gateway/plexplayback -count=1
```

Expected: PASS.

- [x] **Step 5: Commit the playback adapter**

```bash
git add internal/gateway/plexplayback
git commit -m "feat: read bounded Plex playback sessions"
```

### Task 3: Plex next-episode metadata resolver

**Files:**
- Create: `internal/gateway/plexmetadata/gateway.go`
- Create: `internal/gateway/plexmetadata/gateway_test.go`

**Interfaces:**
- Consumes: the same narrow token-source shape and `domain.EpisodeCoordinate`.
- Produces: `Gateway.Next(ctx context.Context, externalShowID string, current domain.EpisodeCoordinate) (domain.EpisodeCoordinate, error)`; no successor returns `domain.ErrNotFound`.

- [x] **Step 1: Write failing metadata hierarchy tests**

Model `/library/metadata/<show-id>/children` season responses and season child episode responses. Prove same-season advancement, coordinate gaps, season transitions, season-0 exclusion, out-of-order provider payloads, duplicate coordinates, terminal show, malformed GUID/keys/counts, 401/403, redirects, oversized payloads, cancellation, and sanitized failures.

```go
next, err := gateway.Next(ctx, "plex://show/5d9c086ce98e47001eb0f520", mustCoordinate(t, 1, 8))
require.NoError(t, err)
require.Equal(t, 2, next.Season())
require.Equal(t, 1, next.Episode())
```

- [x] **Step 2: Run the resolver tests and verify RED**

Run: `go test ./internal/gateway/plexmetadata -count=1`

Expected: build failure because the package does not exist.

- [x] **Step 3: Implement bounded hierarchy traversal**

Validate show IDs with `^plex://show/([0-9a-f]{24})$`, section keys with `^[0-9a-f]{24}$`, and coordinates through the domain constructor. Limit each body to 2 MiB, seasons to 100, episodes per season to 1000, and only traverse season indexes 1 through 99. Sort and deduplicate coordinates, return the least value greater than current, and stop once found.

- [x] **Step 4: Run race tests**

Run:

```bash
go test -race ./internal/domain ./internal/gateway/plexmetadata -count=1
```

Expected: PASS.

- [x] **Step 5: Commit the metadata resolver**

```bash
git add internal/gateway/plexmetadata
git commit -m "feat: resolve exact next Plex episode"
```

### Task 4: Exact manifest path lookup

**Files:**
- Modify: `internal/domain/setup.go`
- Modify: `internal/domain/setup_test.go`
- Modify: `internal/service/setup/service.go`
- Modify: `internal/service/setup/service_test.go`

**Interfaces:**
- Produces: `SetupConfiguration.VirtualPath() (string, error)` and `Service.FindPublishedEpisode(ctx context.Context, virtualPath string) (domain.SetupConfiguration, bool, error)`.
- Consumes: the active validated setup manifest already held by `setup.Service`.

- [x] **Step 1: Write failing exact-path tests**

Prove canonical movie/episode path derivation, exact episode lookup, movie and missing-path rejection, unsafe path rejection, context cancellation, and lookup after restored and acquired manifests.

```go
virtualPath, err := episode.VirtualPath()
require.NoError(t, err)
published, found, err := service.FindPublishedEpisode(ctx, virtualPath)
require.NoError(t, err)
require.True(t, found)
require.Equal(t, episode.Backing(), published.Backing())
```

- [x] **Step 2: Run tests and verify RED**

Run: `go test ./internal/domain ./internal/service/setup -run 'Test.*VirtualPath|Test.*FindPublishedEpisode' -count=1`

Expected: build failure because both methods are missing.

- [x] **Step 3: Implement path derivation and read-only lookup**

Move the existing private path derivation behind `SetupConfiguration.VirtualPath`, retaining validation through `NewMovie` and `NewEpisode`. `FindPublishedEpisode` must read the in-memory manifest under `RLock`, compare only validated episode paths, and return copied public configuration without loading the saved token.

- [x] **Step 4: Run affected race tests**

Run:

```bash
go test -race ./internal/domain ./internal/service/setup -count=1
```

Expected: PASS.

- [x] **Step 5: Commit exact publication lookup**

```bash
git add internal/domain/setup.go internal/domain/setup_test.go internal/service/setup/service.go internal/service/setup/service_test.go
git commit -m "feat: resolve published episodes by Plex path"
```

### Task 5: Atomic Watchlist episode frontier

**Files:**
- Modify: `internal/repository/watchlist/repository.go`
- Modify: `internal/repository/watchlist/repository_test.go`

**Interfaces:**
- Produces:

```go
CanAdvanceEpisode(ctx context.Context, source, externalID, objectID string, current domain.EpisodeCoordinate, observedAfter time.Time) (bool, error)
AdvanceEpisode(ctx context.Context, source, externalID, objectID string, current, next domain.EpisodeCoordinate, observedAfter, now time.Time) (bool, error)
```

- Consumes: existing `watchlist_settings`, `watchlist_queue`, `intent_season`, `intent_episode`, and succeeded publication state; no migration is required.

- [x] **Step 1: Write failing repository transition tests**

Prove one successful S01E01 to S01E02 transition resets the row to pending with no background job or published attachment; disabled acquisition, `off` show policy, stale observation, wrong source/GUID/object/current coordinates, movie row, non-succeeded state, backward/equal next coordinates, and terminal coordinates are no-ops or validation errors. Open two repository instances against one SQLite file and prove concurrent advances have exactly one winner.

After closing and reopening the repository, claim the advanced row and assert its exact request:

```go
claim, err := repository.Claim(ctx, now.Add(time.Second), time.Minute)
require.NoError(t, err)
request, err := claim.SearchRequest()
require.NoError(t, err)
require.Equal(t, 1, request.Season())
require.Equal(t, 2, request.Episode())
```

- [x] **Step 2: Run repository tests and verify RED**

Run: `go test ./internal/repository/watchlist -run 'TestRepository.*AdvanceEpisode' -count=1`

Expected: build failure because the methods do not exist.

- [x] **Step 3: Implement optimistic read and update queries**

Validate all inputs before SQL. `CanAdvanceEpisode` uses one query joining the singleton policy and requiring `last_observed_unix_ms >= observedAfter`. `AdvanceEpisode` repeats every predicate in one `UPDATE`, sets `state='pending'`, stores next coordinates, clears `background_job_id` and `published_object_id`, resets attempts/leases/cooldowns, and returns `RowsAffected()==1`. It must never delete an acquisition job or manifest item.

- [x] **Step 4: Run repository race tests**

Run:

```bash
go test -race ./internal/repository/watchlist -count=1
```

Expected: PASS.

- [x] **Step 5: Commit the durable frontier**

```bash
git add internal/repository/watchlist/repository.go internal/repository/watchlist/repository_test.go
git commit -m "feat: advance durable Watchlist episode frontier"
```

### Task 6: Playback advancement orchestration

**Files:**
- Create: `internal/service/playbackadvance/worker.go`
- Create: `internal/service/playbackadvance/worker_test.go`

**Interfaces:**
- Consumes consumer-defined `PlaybackSnapshotter`, `PublishedEpisodeIndex`, `EpisodeFrontier`, and `NextEpisodeResolver` interfaces matching Tasks 2 through 5.
- Produces: `NewWorker(...)`, `Worker.Process(ctx) (int, error)`, and `Worker.Run(ctx)`.

- [x] **Step 1: Write failing service tests**

Use fakes only at the four architecture boundaries. Prove one qualifying exact session advances once; below-threshold, foreign/missing manifest, metadata mismatch, wrong backing object, disabled/stale frontier, terminal show, duplicate sessions, and canceled context do nothing. Prove transient playback, manifest, metadata, and repository failures return sanitized `ErrUnavailable`, and two concurrent `Process` calls serialize.

```go
count, err := worker.Process(ctx)
require.NoError(t, err)
require.Equal(t, 1, count)
require.Equal(t, mustCoordinate(t, 1, 2), frontier.advanced.Next)
```

- [x] **Step 2: Run service tests and verify RED**

Run: `go test ./internal/service/playbackadvance -count=1`

Expected: build failure because the package does not exist.

- [x] **Step 3: Implement the serialized worker**

Use constants for 120 seconds and 10 percent. Compute freshness as `now - 2*watchlistPollInterval`. For each qualified session: exact manifest lookup, season/episode match, `CanAdvanceEpisode`, metadata `Next`, then optimistic `AdvanceEpisode`. A no-op is not an error. Wrap operation I/O in `OperationTimeout`; `Run` waits `PollInterval` between snapshots and exits on cancellation.

- [x] **Step 4: Run service and neighboring race tests**

Run:

```bash
go test -race ./internal/service/playbackadvance ./internal/service/watchlist ./internal/repository/watchlist -count=1
```

Expected: PASS.

- [x] **Step 5: Commit orchestration**

```bash
git add internal/service/playbackadvance
git commit -m "feat: advance shows from Plex playback state"
```

### Task 7: Runtime, Compose, and operator UI wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`
- Modify: `compose.torbox.yaml`
- Modify: `scripts/test-torbox-compose.sh`
- Modify: `web/src/components/setup-console.tsx`
- Modify: `web/src/components/setup-console.test.tsx`
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/macos-torbox-poc.md`

**Interfaces:**
- Consumes: existing `plexTokenSource`, `cfg.PlexRefreshURL`, setup service, Watchlist repository, HTTP clients, and background `errgroup` lifecycle.
- Produces: validated playback configuration and one background worker in the same BlackPearl binary.

- [ ] **Step 1: Write failing config, wiring, Compose, and UI tests**

Add configuration tests for valid defaults and invalid enablement without Watchlist/refresh/token source, non-positive intervals/timeouts, unsafe metadata URL credentials/query/fragment, and disabled stray configuration. App tests must prove dependencies are wired only when enabled and all background resources close on startup failure/cancellation. Compose tests must prove the feature is enabled, local Plex URL remains credential-free, Plex networks remain disjoint, and no new mount appears.

Update the existing show-policy UI test to require copy that describes one-episode-ahead playback behavior without promising whole-season acquisition.

- [ ] **Step 2: Run targeted tests and verify RED**

Run:

```bash
go test ./internal/config ./cmd/blackpearl -count=1
./scripts/test-torbox-compose.sh
cd web && bun run test -- setup-console.test.tsx
```

Expected: failures because configuration, runtime wiring, Compose settings, and copy are absent.

- [ ] **Step 3: Add validated configuration**

Add fields with these exact defaults:

```go
PlaybackAdvancementEnabled bool          `env:"BLACKPEARL_PLAYBACK_ADVANCEMENT_ENABLED" envDefault:"false"`
PlaybackPollInterval       time.Duration `env:"BLACKPEARL_PLAYBACK_POLL_INTERVAL" envDefault:"30s"`
PlaybackOperationTimeout   time.Duration `env:"BLACKPEARL_PLAYBACK_OPERATION_TIMEOUT" envDefault:"15s"`
PlaybackMetadataURL        string        `env:"BLACKPEARL_PLAYBACK_METADATA_URL" envDefault:"https://metadata.provider.plex.tv"`
```

Enablement requires Watchlist and Plex refresh. The metadata URL must be absolute HTTP(S) without user info, query, or fragment.

- [ ] **Step 4: Wire one lifecycle-owned playback worker**

Construct both gateways from the existing token source, use the setup service as exact manifest index and the Watchlist repository as frontier, then add `playbackWorker.Run(backgroundContext)` to the existing group. Startup errors must close the repository exactly once. Set Compose enablement to `true`; durable Watchlist policy remains the authorization gate.

- [ ] **Step 5: Update the paired UI copy and docs**

Keep the existing control, but change its description from an S01E01-only pilot to: start with S01E01, then keep exactly one next episode queued only after real playback. Explicitly state that removing the show or turning the policy off stops future advancement and that BlackPearl never requests a whole season.

- [ ] **Step 6: Run targeted tests until GREEN**

Run:

```bash
go test -race ./internal/config ./cmd/blackpearl -count=1
./scripts/test-torbox-compose.sh
cd web && bun run lint && bun run test
```

Expected: PASS.

- [ ] **Step 7: Commit runtime wiring**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/blackpearl/app.go cmd/blackpearl/app_test.go compose.torbox.yaml scripts/test-torbox-compose.sh web/src/components/setup-console.tsx web/src/components/setup-console.test.tsx README.md docs/architecture.md docs/macos-torbox-poc.md
git commit -m "feat: run playback-driven episode advancement"
```

### Task 8: Full verification, independent review, and live macOS acceptance

**Files:**
- Modify: `docs/acceptance-evidence.md`
- Modify: this plan to check completed steps.

**Interfaces:**
- Consumes: the complete runtime from Tasks 1 through 7 and the isolated live macOS stack.
- Produces: reproducible automated and live evidence; no temporary Watchlist mutation remains.

- [ ] **Step 1: Run all automated gates**

Run:

```bash
make verify
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
cd web && bun run lint && bun run test && bun run build && bun audit --production
make compose-check portable-compose-check rolling-compose-check torbox-compose-check
```

Expected: race suite PASS, aggregate coverage at least 80%, zero lint/vulnerability/audit findings, 30 or more UI tests PASS, production build PASS, all Compose safety suites PASS.

- [ ] **Step 2: Request independent code review and fix every Critical or Important finding**

Review the full feature range from `2703794` through the implementation head. Re-run focused race tests after each test-first correction and obtain a ready verdict.

- [ ] **Step 3: Preserve the live Watchlist baseline**

Using the already logged-in Brave session, record the Watchlist's pre-test item GUID set without printing credentials. Add only licensed *MariposaHD* temporarily and confirm one show appears while the prior items remain.

- [ ] **Step 4: Prove S01E01 deduplication and one S01E02 transition**

Rebuild only BlackPearl with the feature enabled. Wait for Watchlist observation to mark the already published S01E01 frontier succeeded without creating a duplicate provider job. Resume S01E01 past the threshold in Brave and assert one durable S01E02 request, no S01E03 request, and one selected candidate plan.

- [ ] **Step 5: Prove provider-backed logical S01E02**

Wait for the existing acquisition worker to publish S01E02. Verify canonical NFS logical size, exact start/middle/tail ranges against the licensed origin, partial cache usage below complete-file size, Plex TV scan, and metadata indexing. If providers return no exact S01E02, retain and document the exact durable frontier/job, improve only provider resolution within the same intent, and repeat; never skip to another episode.

- [ ] **Step 6: Prove Direct Play, seeks, and restart recovery in Brave**

Play S01E02 in Brave, assert `readyState == 4`, no video error, time advancement, forward and backward seeking, and a Plex `MDE=1000,Direct play OK` decision. Restart only BlackPearl, confirm Plex's container age is unchanged, then resume and seek again through the same logical file.

- [ ] **Step 7: Restore external state and record evidence**

Remove the temporary MariposaHD Watchlist item, verify the exact baseline GUID set is restored, and do not remove published BlackPearl test episodes. Update `docs/acceptance-evidence.md` with job state, logical/cache sizes, Direct Play decision, seek/restart evidence, review verdict, and explicit separation between macOS proof and pending Windows/native-Linux acceptance.

- [ ] **Step 8: Commit acceptance evidence**

```bash
git add docs/acceptance-evidence.md docs/superpowers/plans/2026-08-14-playback-state-episode-advancement.md
git commit -m "docs: record playback episode advancement acceptance"
```
