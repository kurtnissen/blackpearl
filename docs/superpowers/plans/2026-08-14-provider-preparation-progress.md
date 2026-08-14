# Provider Preparation Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist and display provider-neutral preparation progress while TorBox obtains a durable acquisition release.

**Architecture:** Add one validated inspection value to the zero-provider-specific acquisition domain. Both acquisition services consume that value; the durable worker commits monotonic progress on not-ready polls, while the TorBox gateway maps its 0–1 response field into the value.

**Tech Stack:** Go 1.24, SQLite, `go test -race`, testify, TorBox HTTP JSON gateway, existing OpenAPI/UI progress surface.

## Global Constraints

- Keep the provider contract generic; no TorBox type enters the service or domain.
- Persist only an integer percentage and never credentials, URLs, or provider response bodies.
- Progress is 0–99 while preparing, 100 when ready/succeeded, monotonic within one selected candidate, and reset by existing candidate fallback.
- Preserve current not-ready, stalled, unauthorized, missing, ambiguous-object, publication, and playback behavior.

---

### Task 1: Provider-neutral inspection value

**Files:**
- Modify: `internal/acquisition/result.go`
- Modify: `internal/acquisition/result_test.go`

**Interfaces:**
- Produces: `NewPreparationInspection([]domain.MediaCandidate, int) (PreparationInspection, error)`
- Produces: `PreparationInspection.Candidates() []domain.MediaCandidate`
- Produces: `PreparationInspection.Progress() int`

- [ ] **Step 1: Write failing validation and defensive-copy tests**

Add table tests proving 0, 42, and 100 are accepted, -1 and 101 are rejected,
and mutating either the input or returned candidate slice cannot mutate the
inspection value.

- [ ] **Step 2: Run the focused test and observe RED**

Run: `go test ./internal/acquisition -run PreparationInspection -count=1`

Expected: compilation fails because `NewPreparationInspection` is undefined.

- [ ] **Step 3: Add the immutable value**

Implement a private candidate slice and percentage. Validate every candidate
through `domain.NewMediaCandidate`, reject extension mismatches, reject values
outside 0 through 100, and return defensive copies.

- [ ] **Step 4: Run the focused package tests and observe GREEN**

Run: `go test -race ./internal/acquisition -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/acquisition/result.go internal/acquisition/result_test.go
git commit -m "feat: add provider preparation inspection"
```

### Task 2: TorBox progress mapping

**Files:**
- Modify: `internal/gateway/torbox/gateway.go`
- Modify: `internal/gateway/torbox/inspect.go`
- Modify: `internal/gateway/torbox/inspect_test.go`

**Interfaces:**
- Consumes: `acquisition.NewPreparationInspection(candidates, progress)`
- Produces: `InspectCreatedTorrent(context.Context, acquisition.CreatedObject) (acquisition.PreparationInspection, error)`

- [ ] **Step 1: Write failing gateway tests**

Cover response progress `0.426` becoming 42 with `ErrNotReady`, an unfinished
response at `1.0` being capped at 99, ready progress becoming 100, a stalled
response preserving valid progress, and `-0.1`, `1.1`, an overflowing JSON
number, or a JSON type mismatch returning a sanitized decode/validation error.

- [ ] **Step 2: Run the focused test and observe RED**

Run: `go test ./internal/gateway/torbox -run InspectCreatedTorrent -count=1`

Expected: assertions fail because the gateway does not expose progress.

- [ ] **Step 3: Decode and normalize progress**

Add `Progress float64 \`json:"progress"\`` to `torrentRecord`. Reject values
outside 0 through 1; `encoding/json` rejects non-finite and overflowing input.
Convert not-ready values with `min(99, int(math.Floor(value * 100)))`; force 100
for a ready object. Construct the inspection before returning the existing
readiness sentinel so callers retain progress with an error.

- [ ] **Step 4: Run gateway tests and observe GREEN**

Run: `go test -race ./internal/gateway/torbox -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/torbox/gateway.go internal/gateway/torbox/inspect.go internal/gateway/torbox/inspect_test.go
git commit -m "feat: report TorBox preparation progress"
```

### Task 3: Durable monotonic progress

**Files:**
- Modify: `internal/service/acquisition/service.go`
- Modify: `internal/service/acquisition/service_test.go`
- Modify: `internal/service/acquisitionjob/worker.go`
- Modify: `internal/service/acquisitionjob/worker_test.go`
- Modify: `docs/architecture.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Consumes: `Preparer.InspectCreatedTorrent(...) (acquisition.PreparationInspection, error)`
- Consumes: `WorkerQueue.Defer(..., progress int, ...)`
- Preserves: existing HTTP `AcquisitionJob.progress` field and UI progress card.

- [ ] **Step 1: Write failing worker progress tests**

Have the fake provider return not-ready inspections at 37 then 22. Assert the
first deferral persists 37 and the second preserves 37. Add a fallback test
showing `Advance` resets progress to zero and retain success-at-100 coverage.

- [ ] **Step 2: Run focused service tests and observe RED**

Run: `go test ./internal/service/acquisition ./internal/service/acquisitionjob -run 'Progress|Acquire' -count=1`

Expected: compilation or assertions fail because services still consume only
candidate slices and the worker defers with its old progress.

- [ ] **Step 3: Update consumers and monotonic deferral**

Change both narrow gateway interfaces to the new result. The synchronous
service passes `inspection.Candidates()` to selection. The worker computes
`max(claim.Job().Progress(), inspection.Progress())` for `ErrNotReady` and
passes that value to `Defer`; all other transition policies stay unchanged.

- [ ] **Step 4: Prove repository/API durability**

Run existing SQLite repository and handler tests with the worker tests. Their
existing progress snapshot/API assertions must pass without schema changes.

Run: `go test -race ./internal/repository/acquisitionjob ./internal/handler/setup ./internal/service/acquisition ./internal/service/acquisitionjob -count=1`

Expected: PASS.

- [ ] **Step 5: Update architecture and acceptance evidence**

Document provider-neutral progress, monotonic same-candidate behavior, and the
TorBox mapping. Do not claim live in-progress evidence unless an actual active
provider object is observed during verification.

- [ ] **Step 6: Run all release gates**

Run:

```bash
make verify
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
(cd web && bun install --frozen-lockfile && bun run lint && bun run test && bun run build && bun audit --production)
BLACKPEARL_SETUP_BOOTSTRAP_TOKEN=$(tr -d '\r\n' <runtime/torbox-bootstrap) make compose-check portable-compose-check rolling-compose-check torbox-compose-check
git diff --check
```

Expected: every command exits zero, Go coverage remains at least 80%, and no
Compose safety boundary changes.

- [ ] **Step 7: Commit**

```bash
git add internal/service internal/gateway/torbox internal/acquisition docs/architecture.md docs/acceptance-evidence.md
git commit -m "feat: persist provider preparation progress"
```
