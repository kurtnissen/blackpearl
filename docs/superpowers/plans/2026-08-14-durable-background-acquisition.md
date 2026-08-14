# Durable background acquisition implementation plan

**Goal:** Turn an explicit uncached request into a restart-safe background job
that prepares through TorBox and publishes to Plex automatically.

### Task 1: Define and persist the job state machine

**Files:** `internal/acquisition/job.go`, `internal/repository/acquisitionjob/*`

- [x] Add immutable job, state, selection, claim, and transition value objects.
- [x] Add embedded SQLite migrations, active-intent deduplication, lease claims,
  version-checked transitions, list/get, and restart recovery.
- [x] Prove invalid transitions, stale leases, redaction, and restart behavior
  with repository tests.
- [x] Commit `feat: persist durable acquisition jobs`.

### Task 2: Materialize and prepare provider content safely

**Files:** `internal/acquisition/material.go`, `internal/gateway/prowlarr/*`,
`internal/gateway/torbox/*`

- [x] Add a bounded transient torrent-input value object.
- [x] Add same-origin Prowlarr magnet/`.torrent` materialization.
- [x] Verify raw bencoded torrent info hashes before provider mutation.
- [x] Add TorBox reconcile-by-hash and explicit allow-download creation without
  automatic mutation retries.
- [x] Commit `feat: prepare uncached TorBox releases`.

### Task 3: Add the durable worker and coordinator wiring

**Files:** `internal/service/acquisitionjob/*`,
`internal/service/acquisition/coordinator.go`, `cmd/blackpearl/app.go`

- [x] Resolve and persist a ranked release before creation.
- [x] Reconcile before create, poll preparing objects, select playable media,
  and publish through the existing transaction.
- [x] Classify retryable, terminal, unauthorized, and ambiguous outcomes.
- [x] Run the worker from the process lifecycle and prove restart recovery.
- [x] Commit `feat: run background acquisition jobs`.

### Task 4: Add the paired API and setup UI

**Files:** `api/openapi.yaml`, `internal/api/*.gen.go`,
`internal/handler/setup/*`, `web/src/lib/api.*`,
`web/src/components/setup-console.*`, `web/src/app/globals.css`

- [x] Specify submit/list/get operations and strict schemas.
- [x] Preserve the existing strict JSON decoder and domain validation at runtime.
- [x] Add authorized handlers and safe error mapping.
- [x] Add the explicit `Prepare through TorBox` action and restart-safe progress
  card with polling.
- [x] Commit `feat: show background acquisition progress`.

### Task 5: Verify the end-to-end product slice

**Files:** `docs/acceptance-evidence.md`, `docs/macos-torbox-poc.md`, `README.md`

- [ ] Run focused tests after every red/green/refactor cycle.
- [ ] Run the full Go, frontend, security, vulnerability, and Compose gates.
- [ ] Rebuild the macOS rolling stack and verify ready/restart behavior.
- [ ] Submit a legally usable uncached release, observe background completion,
  automatic Plex publication, Direct Play, forward seek, and backward seek in
  Brave.
- [ ] Confirm BlackPearl cache remains bounded and no complete media file exists.
- [ ] Request a focused architecture/security review and resolve all critical or
  important findings.
- [ ] Commit `docs: verify durable background acquisition`.
