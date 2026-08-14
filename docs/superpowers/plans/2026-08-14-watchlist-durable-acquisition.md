# Watchlist Durable Acquisition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route opted-in Plex Watchlist movies through BlackPearl's restart-safe background acquisition queue and reconcile them to a published Plex file.

**Architecture:** The Watchlist worker submits and monitors durable acquisition jobs but never accesses a provider itself. The Watchlist repository persists the linked job ID under its existing lease/version contract; the acquisition-job worker remains the only owner of search, TorBox preparation, publication, and Plex refresh.

**Tech Stack:** Go 1.24+, SQLite, OpenTelemetry, testify, Docker Compose, Plex, TorBox, Brave.

## Global Constraints

- Watchlist access remains read-only and automatic acquisition remains disabled by default.
- Existing Watchlist items are never authorized by enabling automatic acquisition; only movies first observed after the startup baseline are eligible.
- Only movies become acquisition intent; shows remain aggregate observation-only.
- Provider credentials, titles, external IDs, job IDs, and object IDs never appear in Watchlist API responses or logs.
- Live provider verification uses legally redistributable open media.
- Every new I/O method accepts `context.Context` first and all tests run under `-race`.

---

### Task 1: Persist the durable acquisition link

**Files:**
- Modify: `internal/acquisition/queue.go`
- Modify: `internal/acquisition/queue_test.go`
- Create: `internal/repository/watchlist/migrations/002_background_job.sql`
- Modify: `internal/repository/watchlist/repository.go`
- Modify: `internal/repository/watchlist/repository_test.go`

**Interfaces:**
- Produces: `NewWatchlistJobClaim(item WatchlistItem, leaseVersion int64, attempt int, jobID string) (WatchlistClaim, error)`
- Produces: `(WatchlistClaim).BackgroundJobID() string`
- Produces: `(Repository).AttachJob(context.Context, WatchlistClaim, string, time.Time) error`
- Produces: `(Repository).DeferJob(context.Context, WatchlistClaim, time.Time) error`

- [x] Write failing domain tests for valid and malformed 32-character lowercase hexadecimal job IDs.
- [x] Run `go test ./internal/acquisition -run Watchlist` and confirm the new tests fail.
- [x] Add the optional linked-job value to `WatchlistClaim` while preserving the existing constructor for unlinked claims.
- [x] Write failing repository tests that attach a job, reclaim it only after the reconciliation delay, preserve it across reopen, and reject stale attach/defer operations.
- [x] Run `go test ./internal/repository/watchlist -run 'Job|Lease'` and confirm the new tests fail.
- [x] Add migration `002_background_job.sql`, scan the job ID during claims, and implement version-checked attach/defer transitions.
- [x] Run `go test -race ./internal/acquisition ./internal/repository/watchlist` and confirm it passes.
- [x] Commit `feat: link watchlist requests to durable jobs`.

### Task 2: Replace cached-only Watchlist acquisition with durable orchestration

**Files:**
- Modify: `internal/service/watchlist/worker.go`
- Modify: `internal/service/watchlist/worker_test.go`

**Interfaces:**
- Consumes: acquisition-job `Submit(context.Context, acquisition.SearchRequest)` and `Get(context.Context, string)`.
- Consumes: Watchlist `Claim`, `AttachJob`, `DeferJob`, and `Complete`.
- Produces: a serialized worker whose `ProcessOne` performs exactly one submit or reconciliation transition.

- [x] Replace the fake cached acquirer tests with failing tests for submit, active defer, success, no-release/stalled cooldown, manual review, deduplication, and cancellation-safe attachment.
- [x] Run `go test ./internal/service/watchlist` and confirm the new behavior fails.
- [x] Define the consumer-owned `JobManager` interface and update `WorkerOptions` with a bounded reconciliation interval.
- [x] Implement submit-or-reconcile logic and map only privacy-safe durable job states/error codes to Watchlist outcomes.
- [x] Keep completion commits on bounded `context.WithoutCancel` contexts so an attached provider mutation is never lost on shutdown.
- [x] Run `go test -race ./internal/service/watchlist` and confirm it passes.
- [x] Commit `feat: acquire watchlist movies durably`.

### Task 3: Wire the process and preserve safe defaults

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`
- Modify: `compose.torbox.yaml`

**Interfaces:**
- Consumes: the existing process-lifetime acquisition-job manager and worker.
- Produces: `BLACKPEARL_WATCHLIST_RECONCILE_INTERVAL` with a safe bounded default.

- [x] Write failing configuration tests for a 5-second to 10-minute reconciliation interval that is used only when Watchlist acquisition is enabled.
- [x] Write an app integration test where a Watchlist movie enters the durable job queue without invoking cached-only acquisition.
- [x] Run focused config and app tests and confirm they fail.
- [x] Wire the Watchlist worker to `acquisitionJobManager`; remove its direct cached coordinator dependency; keep Compose acquisition opt-in `false`.
- [x] Persist immutable automatic eligibility and prove startup baseline items never enter the acquisition queue.
- [x] Update the paired UI to explain post-baseline eligibility and uncached TorBox preparation.
- [x] Run `go test -race ./internal/config ./cmd/blackpearl` and confirm it passes.
- [x] Commit `feat: wire durable watchlist acquisition`.

### Task 4: Update product copy and acceptance evidence

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/macos-torbox-poc.md`
- Modify: `docs/acceptance-evidence.md`
- Modify: `docs/superpowers/plans/2026-08-14-watchlist-durable-acquisition.md`

**Interfaces:**
- Produces: operator instructions that state the uncached provider-mutation semantics before opt-in.

- [ ] Replace cached-only Watchlist wording with durable, explicit-opt-in behavior and legal-source guidance.
- [ ] Run `rg -n 'cached-only.*Watchlist|Watchlist.*cached-only' README.md docs compose.torbox.yaml` and resolve stale claims.
- [ ] Run `make verify`, `golangci-lint run`, `govulncheck ./...`, and the frontend lint/test/build/audit commands.
- [ ] Run all Compose safety checks documented by the repository.
- [ ] Rebuild the macOS stack and use Brave to submit one legal open-media Watchlist request.
- [ ] Verify durable job recovery, Plex publication, Direct Play, forward/backward seeks, and rolling cache bounds.
- [ ] Record exact non-secret evidence and check every task in this plan.
- [ ] Commit `docs: verify watchlist durable acquisition`.
