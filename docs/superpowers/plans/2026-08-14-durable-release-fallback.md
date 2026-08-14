# Durable Release Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make durable background acquisition prefer cached releases and advance safely through up to five authorized torrent candidates without repeating or ambiguously cleaning provider mutations.

**Architecture:** A bounded candidate plan is persisted beside each acquisition job. The worker records provider-object ownership, deletes only exact BlackPearl-created failed objects, and atomically selects the next candidate across restarts. Existing filesystem, setup publication, Watchlist authorization, and cache boundaries remain unchanged.

**Tech Stack:** Go 1.24+, SQLite via `database/sql` and modernc, TorBox HTTP gateway, OpenTelemetry, testify, Docker Compose, Bun/Vitest.

## Global Constraints

- Persist at most five normalized torrent candidates; never persist credentials, locators, torrent bytes, magnets, signed URLs, or provider response bodies.
- Automatic Watchlist acquisition remains opt-in, serialized, and post-baseline only.
- Delete only an exact provider object proven to have been created by the same BlackPearl job.
- Never automatically retry an ambiguous destructive provider request; enter manual review.
- Existing jobs without a candidate plan retain legacy behavior and never infer ownership.
- Published provider objects are never fallback-cleaned.
- Every production behavior follows RED, GREEN, REFACTOR and passes `go test -race`.

---

### Task 1: Candidate-plan domain values

**Files:**
- Modify: `internal/acquisition/job.go`
- Modify: `internal/acquisition/job_test.go`

**Interfaces:**
- Produces: `CandidateOutcome`, `JobCandidate`, `NewJobCandidate(JobSelection, int, CandidateOutcome)`, `MaximumJobCandidates`, `JobSnapshotInput.SelectedCandidateOrdinal`, `JobSnapshotInput.CreatedByJob`, `AcquisitionJob.SelectedCandidateOrdinal()`, and `AcquisitionJob.CreatedByJob()`.

- [x] **Step 1: Write failing domain tests** proving ordinals `0..4`, allowed outcomes, locator stripping through `JobSelection`, selected-ordinal stage invariants, and the rule that `CreatedByJob` requires a preparing/succeeded job with a created object.
- [x] **Step 2: Run RED:** `go test ./internal/acquisition -run 'Test(NewJobCandidate|AcquisitionJobCandidateProvenance)' -count=1`. Expected: missing types and fields.
- [x] **Step 3: Implement the values** with `MaximumJobCandidates = 5`, outcomes `pending`, `selected`, `stalled`, `missing`, and `unplayable`; copy validated selections instead of exposing mutable slices.
- [x] **Step 4: Run GREEN:** `go test -race ./internal/acquisition -count=1`.
- [x] **Step 5: Commit:** `git commit -m "feat: model durable release candidates"` with only the two domain files.

### Task 2: Atomic candidate-plan persistence

**Files:**
- Create: `internal/repository/acquisitionjob/migrations/002_candidate_fallback.sql`
- Modify: `internal/repository/acquisitionjob/repository.go`
- Modify: `internal/repository/acquisitionjob/repository_test.go`

**Interfaces:**
- Consumes: Task 1 candidate values.
- Produces: `Plan(ctx, claim, []JobCandidate, now) error`, `AttachPrepared(ctx, claim, created, createdByJob, now) error`, `Candidates(ctx, jobID) ([]JobCandidate, error)`, and `Advance(ctx, claim, outcome, terminalCode, now) (bool, error)` where `bool` reports another selected candidate and exhaustion transitions directly to failed.

- [x] **Step 1: Write failing repository tests** for a five-row atomic plan, ordered reload, cached restart snapshots, attach ownership, one-winner stale-claim rejection, advancement clearing created fields, exhaustion, and a migrated legacy selection with no candidate rows.
- [x] **Step 2: Run RED:** `go test ./internal/repository/acquisitionjob -run 'TestRepository(Plans|Advances|Migrates)' -count=1`. Expected: migration/method failures.
- [x] **Step 3: Add migration 002** with `selected_candidate_ordinal INTEGER NOT NULL DEFAULT -1`, `created_by_job INTEGER NOT NULL DEFAULT 0 CHECK (...)`, and `acquisition_job_candidates` keyed by `(job_id, ordinal)` with a cascading foreign key, unique `(job_id, info_hash)`, bounded ordinal check, and bounded outcome check.
- [x] **Step 4: Implement transactional methods.** `Plan` must insert all candidates and update queued→selected in one transaction under the lease. `Advance` must mark the current row and either select the smallest pending ordinal while clearing provider-object fields/provenance, or transition directly to failed with `terminalCode` when exhausted. Every update includes job ID, lease version, unexpired lease, and expected state.
- [x] **Step 5: Extend row scanning** so ownership and selected ordinal are validated by the domain snapshot while existing rows default safely.
- [x] **Step 6: Run GREEN:** `go test -race ./internal/repository/acquisitionjob -count=1`.
- [x] **Step 7: Commit:** `git commit -m "feat: persist durable release fallback plans"`.

### Task 3: Exact TorBox cleanup gateway

**Files:**
- Create: `internal/gateway/torbox/delete.go`
- Create: `internal/gateway/torbox/delete_test.go`

**Interfaces:**
- Produces: `DeleteCreatedTorrent(ctx context.Context, created acquisition.CreatedObject) error`.

- [x] **Step 1: Write failing TLS gateway tests** asserting `POST /v1/api/torrents/controltorrent`, bearer authorization, exact positive numeric `torrent_id`, operation `delete`, one request only, redirect refusal, bounded response, cancellation, success mapping, already-missing mapping, sanitized rejected detail, and rejection of a non-TorBox provider before I/O.
- [x] **Step 2: Run RED:** `go test ./internal/gateway/torbox -run TestGatewayDeleteCreatedTorrent -count=1`. Expected: method missing.
- [x] **Step 3: Implement deletion** using the existing isolated HTTP client and bounded JSON envelope. Treat explicit success and provider not-found as definite cleanup; return normalized errors for every other response. Do not retry.
- [x] **Step 4: Run GREEN:** `go test -race ./internal/gateway/torbox -count=1`.
- [x] **Step 5: Commit:** `git commit -m "feat: delete exact failed TorBox objects"`.

### Task 4: Cached-first multi-provider planning

**Files:**
- Modify: `internal/service/acquisitionjob/worker.go`
- Modify: `internal/service/acquisitionjob/worker_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Consumes: `Preparer.CachedTorrents(ctx, releases)` and `WorkerQueue.Plan(...)`.
- Produces: a queued-job resolution that persists a bounded cached-first candidate plan before any provider mutation.

- [x] **Step 1: Write failing worker tests** where search returns six ranked releases, cache lookup returns ordinals 2 and 4, and the persisted plan contains cached releases first, preserves relative rank, deduplicates hashes, and caps at five. Add cache-lookup unavailable and no-release mappings.
- [x] **Step 2: Run RED:** `go test ./internal/service/acquisitionjob -run 'TestWorker(Plans|Prioritizes)' -count=1`. Expected: queue/preparer contracts missing.
- [x] **Step 3: Extend `Preparer` and `WorkerQueue`** with cached lookup and plan persistence. Replace first-release `Select` with candidate construction and one `Plan` call.
- [x] **Step 4: Wire combined search** with `resolver.NewSearcher(openMediaGateway, searchGateway)` when open media is enabled so either authorized source can contribute. Preserve Prowlarr readiness through the acquisition settings path and retain operation timeout bounds.
- [x] **Step 5: Update app fakes and integration tests** to prove provider configuration still restores and a submitted job reaches selected with a persisted plan.
- [x] **Step 6: Run GREEN:** `go test -race ./internal/service/acquisitionjob ./cmd/blackpearl -count=1`.
- [x] **Step 7: Commit:** `git commit -m "feat: plan cached-first acquisition candidates"`.

### Task 5: Ownership-aware fallback transitions

**Files:**
- Modify: `internal/service/acquisitionjob/worker.go`
- Modify: `internal/service/acquisitionjob/worker_test.go`
- Modify: `internal/repository/acquisitionjob/repository.go`
- Modify: `internal/repository/acquisitionjob/repository_test.go`

**Interfaces:**
- Consumes: `AttachPrepared(..., createdByJob, ...)`, `Advance(...)`, and `DeleteCreatedTorrent(...)`.
- Produces: safe retry of stalled, missing, and unplayable candidates; terminal exhaustion or manual review on ambiguous cleanup.

- [x] **Step 1: Write failing worker tests** for: existing-object stall advances without deletion; owned-object stall deletes then advances; owned missing object advances without deletion; unplayable owned object deletes then advances; second candidate publishes successfully; exhausted plan fails; deletion error enters manual review; legacy jobs remain terminal and never delete.
- [x] **Step 2: Run RED:** `go test ./internal/service/acquisitionjob -run 'TestWorker(FallsBack|Cleans|PreservesLegacy)' -count=1`. Expected: current worker terminally fails first candidate.
- [x] **Step 3: Record provenance in prepare.** Reconciliation attaches with false; successful create attaches with true. No other path may set ownership.
- [x] **Step 4: Implement `abandonCandidate`.** Delete only owned objects; after definite cleanup call `Advance`; return selected when another candidate exists; otherwise fail using `stalled` or `no_playable_media`. Any cleanup uncertainty calls `Fail(... ambiguous_mutation, true)`.
- [x] **Step 5: Route inspect outcomes** `ErrStalled`, definite missing, and candidate selection failure through abandonment only when the job has a candidate plan; preserve legacy behavior otherwise.
- [x] **Step 6: Run GREEN:** `go test -race ./internal/service/acquisitionjob ./internal/repository/acquisitionjob -count=1`.
- [x] **Step 7: Commit:** `git commit -m "feat: fall back across durable release candidates"`.

### Task 6: Full integration, documentation, and live acceptance

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/acceptance-evidence.md`
- Modify: `docs/superpowers/plans/2026-08-14-durable-release-fallback.md`

**Interfaces:**
- Produces: final operator evidence that distinguishes automated contracts, live provider exhaustion, and successful Plex publication.

- [x] **Step 1: Run focused repeated race tests:** `go test -race -count=5 ./internal/service/acquisitionjob ./internal/repository/acquisitionjob ./internal/gateway/torbox`.
- [x] **Step 2: Run complete verification:** `make verify`; `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run`; `go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...`.
- [x] **Step 3: Run frontend and Compose gates:** `(cd web && bun run lint && bun run test && bun run build && bun audit --production)` and `make compose-check portable-compose-check rolling-compose-check torbox-compose-check`.
- [x] **Step 4: Rebuild the TorBox stack** with automatic Watchlist intake enabled and verify `/readyz`, NFS roots, manifest restoration, zero active legacy jobs, and unchanged Plex libraries.
- [x] **Step 5: Run one legal open-movie acceptance.** Capture the candidate plan, ownership transitions, exact cleanup of failed owned candidates, final job state, manifest delta, rolling-cache bytes, Plex Direct Play decision, and forward/backward seek evidence. Remove only exact failed test objects.
- [x] **Step 6: Update architecture and evidence** with exact non-secret observations. Leave successful end-to-end acceptance unchecked if all candidates are externally unavailable.
- [x] **Step 7: Verify the worktree and commit:** `git diff --check && git status --short`, then `git commit -m "docs: verify durable release fallback"`.
