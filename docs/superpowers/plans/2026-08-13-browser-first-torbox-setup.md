# Browser-First TorBox Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user start the portable TorBox/Plex stack, paste a token into a localhost setup page, select one MP4/MKV, and expose it to Plex through the existing range-oriented rolling cache and NFS path without a container restart.

**Architecture:** A setup repository securely persists token and selection, a TorBox gateway discovers eligible media, and a setup service prepares and atomically activates a range-backed catalog through a catalog switch. PearlNFS receives an explicit namespace reload, while an embedded Next.js UI talks to thin same-origin setup handlers on BlackPearl's existing HTTP server.

**Tech Stack:** Go 1.24+, SQLite catalog, `net/http`, OpenAPI 3 documentation, NFSv3, TorBox HTTP API, Next.js App Router, React 19, strict TypeScript, Bun, Vitest, Testing Library, Docker Compose.

## Global Constraints

- Do not modify the current FUSE implementation.
- Preserve range-oriented `ReadAt`; no interface may require a complete local file.
- The setup listener is host-loopback only in Compose and mutations require loopback Host, matching Origin, and CSRF.
- Persist the token only in `/var/lib/blackpearl/setup/torbox.token` with mode `0600`; never return or log it.
- Support one selected `.mp4` or `.mkv` file in the Movies library.
- Keep existing persistent and HTTP rolling profiles backward compatible.
- Use test-first red-green-refactor for every production behavior.

---

### Task 1: Secure Setup Domain and Repository

**Files:**
- Create: `internal/domain/setup.go`
- Create: `internal/domain/setup_test.go`
- Create: `internal/repository/setup/repository.go`
- Create: `internal/repository/setup/repository_test.go`

**Interfaces:**
- Produces: `domain.SetupConfiguration`, `domain.MediaCandidate`, `setup.Repository.Load(context.Context)`, and `setup.Repository.Save(context.Context, string, domain.SetupConfiguration)`.

- [ ] **Step 1: Write failing domain tests** for canonical candidate IDs, `.mp4`/`.mkv` extension normalization, positive size, sanitized title, and year range `1888..2100`.
- [ ] **Step 2: Run `go test ./internal/domain -run Setup -count=1`** and confirm undefined setup types fail compilation.
- [ ] **Step 3: Implement zero-dependency domain constructors** with this public shape:

```go
type MediaCandidate struct {
    ObjectID string `json:"objectId"`
    Name string `json:"name"`
    Extension string `json:"extension"`
    Size int64 `json:"size"`
}

type SetupConfiguration struct {
    ObjectID string `json:"objectId"`
    Name string `json:"name"`
    Extension string `json:"extension"`
    Size int64 `json:"size"`
    Title string `json:"title"`
    Year int `json:"year"`
}
```

- [ ] **Step 4: Write failing repository tests** proving missing configuration is `domain.ErrNotFound`, writes survive reopen, token is trimmed only for one trailing newline on read, directory is `0700`, files are `0600`, and errors never include token content.
- [ ] **Step 5: Run `go test ./internal/repository/setup -count=1`** and confirm the repository package is missing.
- [ ] **Step 6: Implement atomic repository writes** through a temp file, `Sync`, `Rename`, and parent-directory `Sync`; cap token reads at 4096 bytes and JSON reads at 64 KiB.
- [ ] **Step 7: Run `go test -race ./internal/domain ./internal/repository/setup -count=1`** and confirm all tests pass.
- [ ] **Step 8: Commit** with `feat: persist browser setup securely`.

### Task 2: Read-Only TorBox Media Discovery

**Files:**
- Modify: `internal/gateway/torbox/gateway.go`
- Modify: `internal/gateway/torbox/gateway_test.go`
- Create: `internal/gateway/torbox/discovery.go`
- Create: `internal/gateway/torbox/discovery_test.go`

**Interfaces:**
- Consumes: `domain.MediaCandidate`.
- Produces: `func (g *Gateway) Discover(ctx context.Context) ([]domain.MediaCandidate, error)`.

- [ ] **Step 1: Write failing table-driven tests** using a TLS test API response containing completed, incomplete, absent, zipped, infected, sample, MP4, MKV, subtitle, zero-size, and hashless files; assert only valid video candidates are returned in case-insensitive name order.
- [ ] **Step 2: Run `go test ./internal/gateway/torbox -run Discover -count=1`** and confirm `Discover` is undefined.
- [ ] **Step 3: Implement discovery** against `/torrents/mylist?bypass_cache=false&limit=1000`, reusing Bearer auth and sanitized JSON helpers, with an 8 MiB bounded response.
- [ ] **Step 4: Add failing tests** proving discovery never calls `/requestdl`, never emits the configured token in returned errors, and stops on context cancellation.
- [ ] **Step 5: Make the tests pass** by mapping only public metadata and wrapping errors without raw response bodies or request URLs.
- [ ] **Step 6: Run `go test -race ./internal/gateway/torbox -count=1`** and confirm all provider tests pass.
- [ ] **Step 7: Commit** with `feat: discover eligible TorBox videos`.

### Task 3: Generic Remote Registration and Switchable Catalog

**Files:**
- Modify: `internal/core/catalog.go`
- Modify: `internal/core/catalog_test.go`
- Create: `internal/core/switch.go`
- Create: `internal/core/switch_test.go`

**Interfaces:**
- Produces: `Catalog.RegisterRemoteMovie(context.Context, domain.SetupConfiguration, domain.BackingRef)`, `CatalogSwitch.Activate(CatalogService) CatalogService`, and `CatalogSwitch.Deactivate()`.
- `CatalogService` contains `List`, `Open`, and `Ready` with the existing signatures.

- [ ] **Step 1: Write failing catalog tests** that register an `.mkv` using its logical size and TorBox backing, preserve the extension in `VirtualPath`, and reject unsupported extensions before persistence.
- [ ] **Step 2: Run `go test ./internal/core -run 'Remote|Switch' -count=1`** and confirm new symbols are undefined.
- [ ] **Step 3: Implement generic registration** using a stable ID `blackpearl-selected-media`, `domain.NewMovie`, and the existing repository/source boundaries.
- [ ] **Step 4: Write failing switch tests** proving inactive list is empty, inactive open/readiness report `domain.ErrNotConfigured`, concurrent readers see either complete old or complete new delegates, and `Activate` returns the old delegate.
- [ ] **Step 5: Implement the switch** with `sync.RWMutex`, copying the delegate under read lock before every call and never holding the lock during I/O.
- [ ] **Step 6: Run `go test -race ./internal/core -count=1`** and confirm all tests pass.
- [ ] **Step 7: Commit** with `feat: atomically switch active catalog`.

### Task 4: Reloadable PearlNFS Namespace

**Files:**
- Modify: `internal/pearlnfs/filesystem.go`
- Modify: `internal/pearlnfs/filesystem_test.go`
- Modify: `internal/pearlnfs/server.go`
- Modify: `internal/pearlnfs/server_test.go`

**Interfaces:**
- Produces: `type Reloadable interface { billy.Filesystem; Reload(context.Context) error }`, `NewReloadable`, and `Server.Reload(context.Context) error`.

- [ ] **Step 1: Write a failing filesystem test** that constructs an empty switch, activates a one-file catalog, calls `Reload`, and confirms the movie appears with the correct size and supports a read near EOF.
- [ ] **Step 2: Run `go test ./internal/pearlnfs -run Reload -count=1`** and confirm `NewReloadable` is undefined.
- [ ] **Step 3: Refactor namespace construction** into a pure `buildEntries([]domain.Media)` function and swap the resulting map under an `RWMutex`; every metadata operation reads one snapshot under the lock.
- [ ] **Step 4: Add a failing rollback test** where an invalid new catalog entry makes reload fail and the prior namespace remains readable.
- [ ] **Step 5: Implement `Server.Reload`** by retaining the `Reloadable` passed to `Start`; preserve `New` and existing server callers.
- [ ] **Step 6: Run `go test -race ./internal/pearlnfs -count=1`** and confirm old and new tests pass.
- [ ] **Step 7: Commit** with `feat: reload PearlNFS catalog safely`.

### Task 5: Transactional Setup Service and Startup Restore

**Files:**
- Create: `internal/service/setup/service.go`
- Create: `internal/service/setup/service_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Produces: `Service.Status`, `Service.Discover`, `Service.Apply`, and a runtime factory that returns a prepared `core.CatalogService` without activation.

- [ ] **Step 1: Write failing setup-mode config tests** proving rolling TorBox config accepts both token and object ID absent only when `BLACKPEARL_SETUP_ENABLED=true`, while partial legacy configuration is rejected.
- [ ] **Step 2: Run `go test ./internal/config -run Setup -count=1`** and confirm setup mode is unsupported.
- [ ] **Step 3: Add typed config fields** `SetupEnabled bool` and `SetupDir string`, defaulting the latter to `/var/lib/blackpearl/setup`, without weakening legacy validation.
- [ ] **Step 4: Write failing service tests** for saved-token discovery, new-token discovery, successful prepare-save-activate-reload ordering, preparation failure retaining old runtime, save failure retaining old runtime, reload failure rolling back activation, and status never containing token data.
- [ ] **Step 5: Run `go test ./internal/service/setup -count=1`** and confirm the package is missing.
- [ ] **Step 6: Implement setup orchestration** with consumer-defined interfaces for repository, gateway factory, runtime factory, switch, and reloader. Limit token input to 4096 bytes and normalize public errors to `ErrUnauthorized`, `ErrUnavailable`, and `ErrInvalidSelection`.
- [ ] **Step 7: Write failing app tests** that start NFS and HTTP with no setup file, restore a saved selection at startup, and continue live in setup-required state if restoration fails.
- [ ] **Step 8: Refactor app wiring** so the switch and reloadable NFS start before setup, while all legacy modes retain their current startup behavior.
- [ ] **Step 9: Run `go test -race ./internal/config ./internal/service/setup ./cmd/blackpearl -count=1`** and confirm all tests pass.
- [ ] **Step 10: Commit** with `feat: activate TorBox setup at runtime`.

### Task 6: Secure Setup HTTP API

**Files:**
- Create: `api/openapi.yaml`
- Create: `internal/handler/setup/handler.go`
- Create: `internal/handler/setup/handler_test.go`
- Modify: `internal/httpserver/server.go`
- Modify: `internal/httpserver/server_test.go`

**Interfaces:**
- Consumes: the setup service's `Status`, `Discover`, and `Apply` methods.
- Produces: `/api/setup/status`, `/api/setup/discover`, and `/api/setup/configuration`.

- [ ] **Step 1: Write `api/openapi.yaml`** with exact request/response schemas, 8 KiB request limits, public error codes, and descriptions stating that tokens are write-only.
- [ ] **Step 2: Write failing handler tests** for each success/error mapping, JSON content type, body size limit, unknown fields, method rejection, loopback Host allowlist, Origin validation, CSRF validation, and absence of the token in bodies and captured logs.
- [ ] **Step 3: Run `go test ./internal/handler/setup -count=1`** and confirm the handler package is missing.
- [ ] **Step 4: Implement thin handlers** using `json.Decoder.DisallowUnknownFields`, `http.MaxBytesReader`, constant-time CSRF comparison, and stable public error envelopes.
- [ ] **Step 5: Add failing middleware tests** for `Cache-Control`, CSP, referrer policy, frame denial, and `/readyz` returning `setup_required` when the switch is inactive.
- [ ] **Step 6: Integrate setup routes into `httpserver.New`** without logging bodies or query strings; preserve existing diagnostics behavior when setup is disabled.
- [ ] **Step 7: Run `go test -race ./internal/handler/setup ./internal/httpserver -count=1`** and confirm all tests pass.
- [ ] **Step 8: Commit** with `feat: expose secure localhost setup API`.

### Task 7: Embedded Next.js Setup Interface

**Files:**
- Create: `web/package.json`
- Create: `web/bun.lock`
- Create: `web/next.config.ts`
- Create: `web/tsconfig.json`
- Create: `web/vitest.config.ts`
- Create: `web/src/app/layout.tsx`
- Create: `web/src/app/page.tsx`
- Create: `web/src/app/globals.css`
- Create: `web/src/components/setup-console.tsx`
- Create: `web/src/components/setup-console.test.tsx`
- Create: `web/src/lib/api.ts`
- Create: `web/src/lib/api.test.ts`
- Create: `internal/handler/setupui/assets.go`
- Create: `internal/handler/setupui/handler.go`
- Create: `internal/handler/setupui/handler_test.go`
- Modify: `internal/httpserver/server.go`

**Interfaces:**
- Produces: a static export under `web/out`, embedded with `go:embed`, served at `/` with history-safe asset handling.

- [ ] **Step 1: Scaffold the strict Next static-export project** with scripts `dev`, `build`, `test`, `lint`, and no runtime server dependency.
- [ ] **Step 2: Write failing API-client tests** proving same-origin URLs, CSRF and setup-authorization propagation, typed errors, and no TorBox token persistence in browser storage.
- [ ] **Step 3: Implement the typed API client** with discriminated UI errors and `cache: "no-store"`.
- [ ] **Step 4: Write failing Testing Library tests** for first setup, discover success, no-results, invalid token, selection, apply, ready state, replace token, change video, token input clearing, and accessible live status.
- [ ] **Step 5: Implement the setup console** as a single workflow with standard form controls, semantic table/list selection, disabled pending actions, and no credential persistence in the browser.
- [ ] **Step 6: Implement the visual system** using ink, paper, and brass tokens; responsive single-column layout; compact operational header; visible keyboard focus; reduced-motion support; and no gradients or glass effects.
- [ ] **Step 7: Run `cd web && bun run lint && bun run test && bun run build`** and confirm strict types, Vitest tests, and static export pass.
- [ ] **Step 8: Write failing Go asset-handler tests** for `/`, Next assets, SPA-safe not-found behavior, MIME types, and traversal rejection.
- [ ] **Step 9: Embed `web/out` and integrate the UI handler** into the control-plane server.
- [ ] **Step 10: Run `go test -race ./internal/handler/setupui ./internal/httpserver -count=1`** and confirm all tests pass.
- [ ] **Step 11: Commit** with `feat: add browser-first TorBox setup UI`.

### Task 8: One-Command Compose and End-to-End Evidence

**Files:**
- Modify: `Dockerfile`
- Modify: `compose.torbox.yaml`
- Modify: `scripts/setup-torbox-poc.sh`
- Modify: `scripts/test-torbox-compose.sh`
- Create: `scripts/test-torbox-setup-compose.sh`
- Modify: `docs/macos-torbox-poc.md`
- Modify: `README.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Produces: `./scripts/torbox-stack.sh start` with no TorBox token in the environment and a paired UI at `http://localhost:8082`.

- [ ] **Step 1: Write failing static Compose tests** asserting no required token/object interpolation, no secret environment source, setup enabled, UI bound to `127.0.0.1`, `/healthz` healthcheck, and unchanged NFS volume driver options.
- [ ] **Step 2: Run `./scripts/test-torbox-compose.sh`** and confirm the legacy required-token assertions fail.
- [ ] **Step 3: Add a Bun frontend build stage** to Docker, copy the static export into the Go build context, and keep the final runtime image free of Bun/Node.
- [ ] **Step 4: Update Compose and helper scripts** so plain `up -d --build` starts setup-required mode without placing a token in environment, command arguments, or Compose secrets.
- [ ] **Step 5: Run `./scripts/test-torbox-compose.sh`** with the non-provider bootstrap fixture and confirm only loopback host ports are published.
- [ ] **Step 6: Run the fake-provider Compose acceptance test** that posts a test token, selects a synthetic remote MP4/MKV, verifies NFS metadata and non-sequential reads, restarts BlackPearl, and verifies selection restoration without storing a complete file.
- [ ] **Step 7: Run browser visual QA** at desktop and narrow widths, capture the first-setup, candidate-list, and ready states, and fix any clipping, unreadable focus, or broken state transition.
- [ ] **Step 8: Run full verification:**

```bash
go test -race -cover ./...
go vet ./...
(cd web && bun run lint && bun run test && bun run build)
./scripts/test-compose-paths.sh
./scripts/test-portable-compose.sh
./scripts/test-rolling-compose.sh
./scripts/test-torbox-compose.sh
./scripts/test-torbox-compose.sh
```

- [ ] **Step 9: Update documentation and acceptance evidence** with exact commands, observed results, security boundaries, and a clear distinction between fake-provider evidence and pending live TorBox/Plex evidence when no credential is available.
- [ ] **Step 10: Commit** with `feat: ship one-command TorBox setup stack`.

### Task 9: Review, Live Validation, and Local Integration

**Files:**
- Modify only files required by review findings.

**Interfaces:**
- Produces: reviewed, verified `main` with the setup feature merged locally.

- [ ] **Step 1: Run a requirements review** against `docs/superpowers/specs/2026-08-13-browser-first-torbox-setup-design.md` and fix every objective gap.
- [ ] **Step 2: Run a code-quality and security review** focused on token exposure, transaction rollback, races, range semantics, and Compose portability; fix confirmed findings test-first.
- [ ] **Step 3: Use the verification-before-completion workflow** and rerun the complete verification set from Task 8 on the final commit.
- [ ] **Step 4: If a TorBox credential is available only through the new UI, perform live discovery and one authorized-file range probe without printing the token; otherwise record live validation as pending rather than claiming it.**
- [ ] **Step 5: Fast-forward the reviewed feature branch into local `main`**, verify the exact merged SHA, and leave all unrelated worktrees and Docker stacks untouched.
