# Watchlist Web Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a restart-safe paired WebUI control for automatic Plex Watchlist movie acquisition.

**Architecture:** Store one boolean in the existing Watchlist SQLite repository, consult it inside the atomic claim query and every observer sync, expose it through the paired service/handler, and render one exact typed control in the existing panel.

**Tech Stack:** Go 1.24, SQLite, OpenAPI 3, React 19/TypeScript, Vitest, Docker Compose.

## Global Constraints

- Fresh installs default off; the environment setting seeds only an absent row.
- Disabling does not cancel provider work or delete queue, cache, manifest, or Plex data.
- Existing first-sync baseline and movie-only policy remain unchanged.
- All mutations retain loopback Host, exact Origin, CSRF, bootstrap/session, and bounded JSON checks.

---

### Task 1: Durable atomic policy

**Files:**
- Create: `internal/repository/watchlist/migrations/003_acquisition_policy.sql`
- Modify: `internal/repository/watchlist/repository.go`
- Modify: `internal/repository/watchlist/repository_test.go`
- Modify: repository open call sites

**Interfaces:**
- Produces: `Open(context.Context, string, bool) (*Repository, error)`
- Produces: `AcquisitionEnabled(context.Context) (bool, error)`
- Produces: `SetAcquisitionEnabled(context.Context, bool) error`

- [ ] Write failing tests for default seeding, initialize-once restart behavior, set persistence, and disabled/enabled atomic claims.
- [ ] Run `go test ./internal/repository/watchlist -count=1` and observe RED.
- [ ] Add a singleton `watchlist_settings` migration, initialize with `INSERT OR IGNORE`, implement strict boolean reads/writes, and gate `Claim` in its SQL predicate.
- [ ] Update call sites with an explicit initial policy and run `go test -race ./internal/repository/watchlist ./cmd/blackpearl -count=1` GREEN.
- [ ] Commit as `feat: persist Watchlist acquisition policy`.

### Task 2: Dynamic observer and worker wiring

**Files:**
- Modify: `internal/service/watchlist/observer.go`
- Modify: `internal/service/watchlist/observer_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Queue consumes: `AcquisitionEnabled(context.Context) (bool, error)` and `SetAcquisitionEnabled(context.Context, bool) error`
- Observer produces: `SetAcquisitionEnabled(context.Context, bool) error`

- [ ] Write failing observer tests proving policy changes affect the next post-baseline sync and status, with sanitized read/write failures.
- [ ] Run `go test ./internal/service/watchlist -count=1` and observe RED.
- [ ] Remove the fixed observer option, read policy per sync/status, add the service setter, and always construct the worker when observation is enabled.
- [ ] Run `go test -race ./internal/service/watchlist ./cmd/blackpearl -count=1` GREEN.
- [ ] Commit as `feat: control Watchlist policy at runtime`.

### Task 3: Paired spec-first API

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/handler/setup/acquisition.go`
- Modify: `internal/handler/setup/handler.go`
- Modify: `internal/handler/setup/acquisition_test.go`

**Interfaces:**
- Adds: `PUT /api/watchlist/settings`
- Body: `{ "acquisitionEnabled": boolean }`, no additional properties
- Response: existing `WatchlistStatus`

- [ ] Write failing tests for success, unpaired/foreign-origin/missing-CSRF rejection, malformed/extra JSON rejection, and unavailable service mapping.
- [ ] Run `go test ./internal/handler/setup -run WatchlistSettings -count=1` and observe RED.
- [ ] Extend the narrow service interface, route PUT through existing authorization before `SetAcquisitionEnabled`, and return refreshed aggregate status.
- [ ] Run handler/OpenAPI checks GREEN and commit as `feat: add paired Watchlist settings API`.

### Task 4: Exact WebUI control and release evidence

**Files:**
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/api.test.ts`
- Modify: `web/src/components/setup-console.tsx`
- Modify: `web/src/components/setup-console.test.tsx`
- Modify: `web/src/app/globals.css`
- Modify: `compose.torbox.yaml`
- Modify: `README.md`, `docs/architecture.md`, `docs/acceptance-evidence.md`

- [ ] Write failing typed API and component tests for on/off success, disabled pending state, failure rollback, and exact safety copy.
- [ ] Run `cd web && bun run test` and observe RED.
- [ ] Add the typed PUT client and one ordinary labeled checkbox/button control without storing credentials or policy in browser storage.
- [ ] Keep Compose's initial default false, update docs, and run all Go/frontend/Compose release gates.
- [ ] Rebuild the macOS stack with the current seeded-on environment, toggle off/on in Brave, restart BlackPearl, and prove status/manifest/Watchlist are unchanged.
- [ ] Commit as `feat: manage Watchlist automation in WebUI`.
