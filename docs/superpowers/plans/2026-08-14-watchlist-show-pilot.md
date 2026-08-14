# Watchlist Show Pilot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a newly Watchlisted TV show into one explicit, restart-safe S01E01 acquisition without bulk series downloads.

**Architecture:** The observer converts provider items plus durable policy into immutable `WatchlistObservation` values. SQLite persists exact episode coordinates and gates claims against current policy. The existing Watchlist worker submits the claim's exact `SearchRequest`; provider preparation, publication, range streaming, and Plex refresh remain unchanged.

**Tech Stack:** Go 1.26.6, SQLite/modernc, OpenAPI 3, React 19/TypeScript, Vitest, Docker Compose.

## Global Constraints

- Fresh and migrated databases use show policy `off`.
- Only `pilot` is supported, and it means exactly season 1 episode 1.
- Eligibility and episode coordinates are immutable after first observation.
- Master automation and show policy both gate new and retry show claims.
- Disabling policy never cancels provider work or deletes jobs, cache, manifest, or Plex data.
- No private Watchlist item metadata is returned by the API or UI.
- Existing movie behavior and the first-sync baseline remain unchanged.

---

### Task 1: Provider-neutral policy and immutable episode intent

**Files:**
- Modify: `internal/acquisition/watchlist.go`
- Modify: `internal/acquisition/queue.go`
- Test: `internal/acquisition/watchlist_test.go`
- Test: `internal/acquisition/queue_test.go`

**Interfaces:**
- Produces: `NewWatchlistPolicy(bool, WatchlistShowPolicy) (WatchlistPolicy, error)`
- Produces: `NewWatchlistObservation(WatchlistItem, bool, int, int) (WatchlistObservation, error)`
- Produces: `NewWatchlistIntentClaim(WatchlistObservation, int64, int) (WatchlistClaim, error)`
- Produces: `NewWatchlistIntentJobClaim(WatchlistObservation, int64, int, string) (WatchlistClaim, error)`
- Produces: `WatchlistClaim.SearchRequest() (SearchRequest, error)`

- [ ] Add failing table tests proving policy accepts only `off|pilot`, movie observations require `0,0`, eligible show observations require positive coordinates, observation-only shows require `0,0`, and S01E01 claims produce `NewEpisodeSearch(title, year, 1, 1)`.
- [ ] Run `go test ./internal/acquisition -run 'Watchlist(Policy|Observation|Claim)' -count=1` and confirm failures are caused by missing types and methods.
- [ ] Implement `WatchlistShowPolicy`, immutable `WatchlistPolicy`, immutable `WatchlistObservation`, intent-aware claim constructors, coordinate accessors, and claim-owned search construction. Retain the existing movie claim constructors as wrappers using an eligible movie observation so current callers keep compiling.
- [ ] Run `go test -race ./internal/acquisition -count=1` and commit as `feat: model Watchlist episode intent`.

### Task 2: Persist policy and atomically claim pilot episodes

**Files:**
- Create: `internal/repository/watchlist/migrations/006_show_pilot.sql`
- Modify: `internal/repository/watchlist/repository.go`
- Test: `internal/repository/watchlist/repository_test.go`

**Interfaces:**
- Consumes: `acquisition.WatchlistPolicy`, `acquisition.WatchlistObservation`
- Produces: `Policy(context.Context) (acquisition.WatchlistPolicy, error)`
- Produces: `SetPolicy(context.Context, acquisition.WatchlistPolicy) error`
- Produces: `UpsertObservations(context.Context, []acquisition.WatchlistObservation, time.Time) error`

- [ ] Write failing repository tests for migrated/default `off`, persisted `pilot`, immutable first-seen coordinates, a disabled show claim, an enabled S01E01 claim, policy-off retry gating, and concurrent one-winner show claims.
- [ ] Run `go test ./internal/repository/watchlist -run 'Policy|Pilot|Show' -count=1` and observe the expected failures.
- [ ] Add `show_policy`, `intent_season`, and `intent_episode` with constraints. Implement exact policy reads/writes and observation CRUD. Extend `Claim` to select eligible movies or policy-enabled shows, return intent coordinates, and keep the policy check inside the same SQLite update statement.
- [ ] Keep the legacy bool methods and item upsert wrappers only as narrow compatibility shims until Task 3 switches all consumers.
- [ ] Run `go test -race ./internal/repository/watchlist -count=1` and commit as `feat: persist Watchlist show intent`.

### Task 3: Dynamic observer and exact-intent worker

**Files:**
- Modify: `internal/service/watchlist/observer.go`
- Modify: `internal/service/watchlist/observer_test.go`
- Modify: `internal/service/watchlist/worker.go`
- Modify: `internal/service/watchlist/worker_test.go`
- Modify: `internal/service/setup/service.go`
- Modify: `internal/service/setup/acquired_test.go`

**Interfaces:**
- Queue consumes: `Policy`, `SetPolicy`, `UpsertObservations`
- Observer produces: `SetPolicy(context.Context, acquisition.WatchlistPolicy) error`
- Published index produces: `FindPublished(context.Context, acquisition.SearchRequest) (string, bool, error)`

- [ ] Add failing observer tests for baseline movie/show observations, later pilot eligibility, runtime policy changes, and sanitized policy failures.
- [ ] Add failing worker/setup tests proving an S01E01 claim is submitted exactly, an existing manifest episode deduplicates by show/year/season/episode, and movie deduplication remains unchanged.
- [ ] Run `go test ./internal/service/watchlist ./internal/service/setup -count=1` and verify RED.
- [ ] Make the observer create explicit observations from current policy and baseline state. Make the worker use `claim.SearchRequest()` and the intent-oriented manifest lookup. Invalid intent completes as manual review without provider submission.
- [ ] Remove fixed bool-only repository use from services, run `go test -race ./internal/service/watchlist ./internal/service/setup ./cmd/blackpearl -count=1`, and commit as `feat: acquire Watchlist pilot episodes`.

### Task 4: Paired policy API

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/handler/setup/acquisition.go`
- Modify: `internal/handler/setup/acquisition_test.go`

**Interfaces:**
- `WatchlistStatus.showPolicy`: `off|pilot`
- `PUT /api/watchlist/settings`: `{ "acquisitionEnabled": boolean, "showPolicy": "off"|"pilot" }`

- [ ] Add failing handler tests for pilot success, off success, missing/invalid/extra fields, unpaired/foreign-origin rejection, and sanitized service failure.
- [ ] Run `go test ./internal/handler/setup -run Watchlist -count=1` and verify failures are caused by the absent policy contract.
- [ ] Update OpenAPI schemas and handler input validation. Construct the domain policy before authorization-sensitive service mutation, preserve the existing paired boundary, and return refreshed aggregate status.
- [ ] Run `go test -race ./internal/handler/setup ./cmd/blackpearl -count=1` and commit as `feat: expose Watchlist show policy`.

### Task 5: Non-technical WebUI control

**Files:**
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/api.test.ts`
- Modify: `web/src/components/setup-console.tsx`
- Modify: `web/src/components/setup-console.test.tsx`
- Modify: `web/src/app/globals.css`
- Regenerate: `web/out/**`

**Interfaces:**
- Typed `WatchlistShowPolicy = "off" | "pilot"`
- `setWatchlistPolicy(policy, csrf, authorization): Promise<WatchlistStatusResult>`

- [ ] Add failing API/component tests for sending both policy fields, switching pilot on/off, disabled pending state, error rollback, exact S01E01 safety copy, and no private item metadata.
- [ ] Run `cd web && bun run test` and observe RED.
- [ ] Add a labeled `Start new shows with S01E01` control beneath the master control. Disable it when the master is off or a request is pending. Send the entire policy atomically and replace UI state only after success.
- [ ] Run `cd web && bun run test && bun run lint && bun run build`, visually inspect desktop and narrow layouts, and commit as `feat: manage Watchlist show pilots in WebUI`.

### Task 6: Documentation, release gates, and live macOS acceptance

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/macos-torbox-poc.md`
- Modify: `docs/acceptance-evidence.md`

- [ ] Document exact pilot semantics, policy durability, non-retroactivity, and the later playback-triggered-next-episode boundary.
- [ ] Run `make verify`, pinned golangci-lint, pinned govulncheck, Bun lint/test/build/audit, and all four Compose safety suites.
- [ ] Rebuild the TorBox profile, use the paired browser control to enable pilot mode, restart BlackPearl, and prove the setting persists with the eight-item manifest unchanged.
- [ ] Add one temporary test show to Plex Watchlist only when it can exercise an authorized exact episode path. Prove S01E01 deduplication or provider-backed publication, then remove the temporary item and confirm the three pre-existing Watchlist movies remain unchanged.
- [ ] Confirm an NFS random range from the existing provider-backed movie remains byte-identical and record live evidence without credentials or private identifiers.
- [ ] Commit as `docs: record Watchlist show pilot acceptance`.
