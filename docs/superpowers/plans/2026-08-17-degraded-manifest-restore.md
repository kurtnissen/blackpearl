# Degraded Saved-Manifest Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep BlackPearl's Plex library available from a validated active subset when transient provider outages prevent full saved-manifest restoration, then heal atomically to the complete manifest.

**Architecture:** Preserve the existing full `RuntimeFactory` for setup and acquisition transactions, and add a separate restore-only factory that returns an immutable active manifest with its catalog. Provider gateways expose typed transient availability through `domain.ErrUnavailable`; setup publishes one stable partial snapshot, reports exact availability counts, and keeps retrying until a full snapshot can replace it.

**Tech Stack:** Go 1.24, context-aware range providers, immutable catalog/NFS switcher, OpenAPI YAML, React 19/TypeScript, Bun, Vitest, Docker Compose.

## Global Constraints

- The full persisted manifest and token remain unchanged by startup restore.
- Only explicitly typed transient provider failures may omit a saved item.
- Authentication, missing object, size/validator/license/path validation, and unsupported-provider failures fail closed.
- Manual `Apply` and `PublishAcquired` remain full-manifest, all-or-nothing transactions.
- A partial retry never replaces or shrinks the first active degraded snapshot; only a complete retry replaces it.
- `/readyz` is 200 only when at least one immutable catalog snapshot is active.
- Runtime range reads use the process lifecycle context and the existing shared cache pool.
- The Plex container and all production media/library paths remain untouched.

---

### Task 1: Add typed provider availability errors

**Files:**
- Modify: `internal/domain/media.go`
- Modify: `internal/gateway/torbox/gateway.go`
- Modify: `internal/gateway/internetarchive/files.go`
- Modify: `internal/gateway/internetarchive/source.go`
- Test: `internal/gateway/torbox/gateway_test.go`
- Test: `internal/gateway/torbox/source_test.go`
- Test: `internal/gateway/internetarchive/gateway_test.go`
- Test: `internal/gateway/internetarchive/source_test.go`

**Interfaces:**
- Consumes: existing `domain.ErrUnauthorized`, `domain.ErrNotFound`, and `acquisition.ErrRangeUnplayable` fatal causes.
- Produces: `domain.ErrUnavailable`, a provider-neutral sentinel that only gateways use to identify retryable external availability failures.

- [ ] **Step 1: Write failing gateway classification tests**

Add table cases proving transport failures, HTTP 429, and HTTP 5xx satisfy
`errors.Is(err, domain.ErrUnavailable)`, while 401/403 remain
`domain.ErrUnauthorized`, 404 remains `domain.ErrNotFound` where applicable,
and malformed or integrity-changing responses do not satisfy
`domain.ErrUnavailable`.

```go
func TestGatewayOpenClassifiesTemporaryProviderFailures(t *testing.T) {
    tests := []struct {
        name   string
        status int
    }{
        {name: "rate limited", status: http.StatusTooManyRequests},
        {name: "provider unavailable", status: http.StatusServiceUnavailable},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            // Arrange the existing bounded provider test server to return test.status.
            _, err := gateway.Open(context.Background(), backing)
            require.ErrorIs(t, err, domain.ErrUnavailable)
            require.NotErrorIs(t, err, domain.ErrNotFound)
        })
    }
}
```

- [ ] **Step 2: Run focused tests and confirm the new expectations fail**

Run:

```text
go test -count=1 ./internal/gateway/torbox ./internal/gateway/internetarchive
```

Expected: the new cases fail because temporary network/status errors currently
lose their typed cause.

- [ ] **Step 3: Add the domain sentinel and preserve it at gateway boundaries**

Add the zero-dependency sentinel:

```go
// ErrUnavailable indicates that an external provider operation may succeed on retry.
ErrUnavailable = errors.New("temporarily unavailable")
```

For both providers, preserve request-context cancellation as its original cause.
When `client.Do` fails and `ctx.Err()` is nil, wrap `domain.ErrUnavailable`.
Wrap HTTP 429 and 500-599 with `domain.ErrUnavailable`. Keep the existing typed
authentication, not-found, and unplayable branches ahead of the temporary
status branch.

```go
func temporaryProviderStatus(status int) bool {
    return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}
```

- [ ] **Step 4: Run focused gateway tests**

Run:

```text
go test -race -count=1 ./internal/gateway/torbox ./internal/gateway/internetarchive
```

Expected: PASS with temporary, authentication, missing-object, and integrity
classifications remaining distinct.

- [ ] **Step 5: Commit the provider contract**

```text
git add internal/domain/media.go internal/gateway/torbox internal/gateway/internetarchive
git commit -m "fix: classify temporary range provider failures"
```

---

### Task 2: Add restore-only preparation and stable degraded state

**Files:**
- Modify: `internal/service/setup/service.go`
- Test: `internal/service/setup/service_test.go`
- Test: `internal/service/setup/acquired_test.go`

**Interfaces:**
- Consumes: `domain.ErrUnavailable`, existing `RuntimeFactory`, `Publisher`, and full saved `SetupManifest`.
- Produces: `RestorePreparation`, `RestoreRuntimeFactory`, `ErrDegraded`, `NewWithRestore`, and additive availability fields on `setup.Status`.

- [ ] **Step 1: Write failing restore state-machine tests**

Create two saved configurations and a restore factory fake that can return
full, partial, or fatal preparations. Add tests with these exact contracts:

```go
type RestorePreparation struct {
    Runtime        core.CatalogService
    ActiveManifest domain.SetupManifest
}

type RestoreRuntimeFactory func(
    context.Context,
    string,
    domain.SetupManifest,
) (RestorePreparation, error)
```

The tests must prove:

```go
err := service.Restore(context.Background())
require.ErrorIs(t, err, setupservice.ErrDegraded)
require.ErrorIs(t, err, setupservice.ErrUnavailable)
require.Same(t, partialRuntime, publisher.active)
require.Equal(t, fullSavedManifest, repository.manifest)
require.Equal(t, 2, service.Status().SavedItemCount)
require.Equal(t, 1, service.Status().ActiveItemCount)
require.Equal(t, 1, service.Status().UnavailableItemCount)
require.True(t, service.Status().Degraded)
require.Equal(t, partialManifest.Items, service.Status().SelectedItems)
```

Also add cases for full success, zero-item preparation, a fatal preparation
error, a second different partial result that must not republish, and a later
full result that must replace the partial runtime.

- [ ] **Step 2: Run the service tests and confirm they fail**

Run:

```text
go test -count=1 ./internal/service/setup
```

Expected: compile/test failure because restore preparation and status fields do
not exist.

- [ ] **Step 3: Implement the restore-only boundary without changing existing callers**

Keep `New` source-compatible by adapting the full runtime factory:

```go
func New(
    repository Repository,
    gatewayFactory GatewayFactory,
    runtimeFactory RuntimeFactory,
    publisher Publisher,
    bootstrapToken ...string,
) *Service {
    restoreFactory := func(ctx context.Context, token string, manifest domain.SetupManifest) (RestorePreparation, error) {
        runtime, err := runtimeFactory(ctx, token, manifest)
        return RestorePreparation{Runtime: runtime, ActiveManifest: manifest}, err
    }
    return NewWithRestore(repository, gatewayFactory, runtimeFactory, restoreFactory, publisher, bootstrapToken...)
}
```

`NewWithRestore` sets both factories. Add status fields:

```go
SavedItemCount       int  `json:"savedItemCount"`
ActiveItemCount      int  `json:"activeItemCount"`
UnavailableItemCount int  `json:"unavailableItemCount"`
Degraded             bool `json:"degraded"`
```

Validate every `RestorePreparation`: runtime must be non-nil, active manifest
must be nonempty, active items must appear unchanged and in saved-manifest
order, and active count cannot exceed saved count.

- [ ] **Step 4: Implement stable partial publication and complete healing**

Use `transitionMu` for the entire restore. On the first partial preparation,
call `Ready`, publish, and set active/saved status. Return:

```go
return errors.Join(ErrDegraded, ErrUnavailable)
```

If an active partial manifest already exists, ignore every later partial
preparation and return the same joined signal without publishing. A later full
preparation may pass `Ready`, publish atomically, clear degraded status, and
return nil. Repository writes are forbidden in `Restore`.

- [ ] **Step 5: Keep active playback and durable deduplication views distinct**

Retain `FindPublished` against `Repository.LoadManifest` for durable
acquisition deduplication. Retain `FindPublishedEpisode` against the active
in-memory manifest for playback advancement. Add an explicit degraded test
showing an unavailable saved episode is found by `FindPublished` but not by
`FindPublishedEpisode` until full healing.

- [ ] **Step 6: Run setup service tests with the race detector**

Run:

```text
go test -race -count=1 ./internal/service/setup
```

Expected: PASS, including concurrent `Restore`/`Apply` serialization.

- [ ] **Step 7: Commit the restore state machine**

```text
git add internal/service/setup
git commit -m "feat: restore saved manifests in degraded mode"
```

---

### Task 3: Build full and partial range catalogs from one validator

**Files:**
- Modify: `cmd/blackpearl/app.go`
- Test: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Consumes: `setupservice.NewWithRestore`, `setupservice.RestorePreparation`, `domain.ErrUnavailable`, existing `cache.RangeRouter`, shared `cache.RollingPool`, and `core.Catalog` registration methods.
- Produces: one common `prepareRangeCatalog` helper used by both full and restore factories.

- [ ] **Step 1: Write failing mixed-provider preparation tests**

Extract the per-manifest loop behind a helper whose inputs can use fake range
openers. Tests must cover:

```go
preparation, err := prepareRangeCatalog(ctx, manifest, router, rangeSource, true)
require.NoError(t, err)
require.Equal(t, []domain.SetupConfiguration{first, third}, preparation.ActiveManifest.Items)
require.NoError(t, preparation.Runtime.Ready(ctx))
```

Use three ordered items where the second opener returns
`domain.ErrUnavailable`. Add fatal cases for `domain.ErrUnauthorized`,
`domain.ErrNotFound`, `acquisition.ErrRangeUnplayable`, size mismatch, invalid
catalog registration, and context cancellation. Add a zero-reachable case that
returns `domain.ErrUnavailable` and no runtime.

- [ ] **Step 2: Run focused wiring tests and confirm failure**

Run:

```text
go test -count=1 ./cmd/blackpearl
```

Expected: compile failure until the helper and restore factory exist.

- [ ] **Step 3: Extract one catalog builder with strict partial semantics**

The helper validates `router.Open`, closes metadata, compares exact logical
size, and registers a movie or episode into one new memory catalog. Its only
skip condition is:

```go
if allowPartial && errors.Is(openErr, domain.ErrUnavailable) {
    continue
}
```

Any other open, close, size, or registration error aborts the whole
preparation. Preserve manifest ordering in `ActiveManifest`.

- [ ] **Step 4: Wire full transactions and startup restore separately**

The existing full `runtimeFactory` calls the helper with `allowPartial=false`
and returns only the catalog. The new `restoreRuntimeFactory` calls it with
`allowPartial=true` and returns the active manifest. Construct the service with
`setupservice.NewWithRestore`. Both factories use the same process-lifetime
`rangePool`; neither creates or recovers a second cache root.

- [ ] **Step 5: Prove the retry loop continues after partial publication**

Extend `startSetupRestore` tests so a restorer sequence of
`ErrDegraded+ErrUnavailable`, then nil produces two calls and exits. Verify a
partial signal logs/retries while the active catalog's readiness remains
independent through the switcher.

- [ ] **Step 6: Run wiring and NFS race tests**

Run:

```text
go test -race -count=1 ./cmd/blackpearl ./internal/pearlnfs ./internal/core
```

Expected: PASS, including issued-handle stability across catalog replacement.

- [ ] **Step 7: Commit runtime wiring**

```text
git add cmd/blackpearl/app.go cmd/blackpearl/app_test.go
git commit -m "feat: publish reachable saved media at startup"
```

---

### Task 4: Expose truthful degraded status in the setup API and UI

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/handler/setup/handler_test.go`
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/api.test.ts`
- Modify: `web/src/components/setup-console.tsx`
- Modify: `web/src/components/setup-console.test.tsx`
- Modify: `web/src/app/globals.css` only if the existing warning styles cannot express the state.

**Interfaces:**
- Consumes: additive `setupservice.Status` availability fields.
- Produces: validated `SetupStatus` fields and a visible degraded-ready message.

- [ ] **Step 1: Update the OpenAPI contract and write failing API parser tests**

Make these status fields required, nonnegative integers/boolean, while retaining
optional `selected` and `selectedItems` for setup-required state:

```yaml
required:
  - setupRequired
  - tokenConfigured
  - csrfToken
  - savedItemCount
  - activeItemCount
  - unavailableItemCount
  - degraded
```

Add TypeScript parser tests for a valid degraded response and invalid negative,
fractional, inconsistent, or missing counts. Valid responses require:

```text
activeItemCount == selectedItems.length
unavailableItemCount == savedItemCount - activeItemCount
degraded == (activeItemCount > 0 && unavailableItemCount > 0)
```

- [ ] **Step 2: Run API and component tests and confirm failure**

Run:

```text
cd web && bun run test -- src/lib/api.test.ts src/components/setup-console.test.tsx
```

Expected: new degraded cases fail until the type guard and UI state are added.

- [ ] **Step 3: Parse and retain availability state in the React flow**

Extend `SetupStatus` with exact numeric fields and `degraded`. Validate finite,
nonnegative integers and the invariants above in `isSetupStatus`. When status is
loaded, retain the counts alongside `selectedItems`. After a successful manual
apply or acquired publication, set saved and active counts to the returned
manifest length and clear degraded state.

- [ ] **Step 4: Render degraded readiness without hiding existing controls**

In the ready card, preserve the current Watchlist, change-video, replacement,
and Plex actions. Change the title and add an alert only while degraded:

```tsx
<h3>{availability.degraded ? "BlackPearl is partially ready" : "BlackPearl is ready"}</h3>
{availability.degraded && (
  <p role="status" className="status-note status-note--warning">
    {availability.activeItemCount} of {availability.savedItemCount} files are available. BlackPearl is retrying the other {availability.unavailableItemCount} automatically.
  </p>
)}
```

The Plex library count continues to use `selectedItems.length`, which is the
active snapshot.

- [ ] **Step 5: Add handler and rendered-flow assertions**

Handler tests assert exact JSON counts and no secret/provider errors. Component
tests assert the partial-ready title and exact copy for 12 active of 15 saved,
then assert normal ready copy for a full status.

- [ ] **Step 6: Run frontend and handler verification**

Run:

```text
go test -race -count=1 ./internal/handler/setup
cd web && bun run lint && bun run test && bun run build
```

Expected: PASS with no TypeScript casts or `any` additions.

- [ ] **Step 7: Commit the status experience**

```text
git add api/openapi.yaml internal/handler/setup/handler_test.go web/src
git commit -m "feat: report degraded library availability"
```

---

### Task 5: Verify repository behavior and live macOS recovery

**Files:**
- Modify: `docs/acceptance-evidence.md`
- Modify: `docs/macos-torbox-poc.md` if the operator flow changes visibly.

**Interfaces:**
- Consumes: the complete degraded-restore feature.
- Produces: reproducible local and CI evidence with unresolved portability and Plex-login gates stated explicitly.

- [ ] **Step 1: Run the whole local quality gate**

Run:

```text
git diff --check
go test -race -count=1 ./...
go vet ./...
golangci-lint run
cd web && bun run lint && bun run test && bun run build
```

Expected: every command exits zero.

- [ ] **Step 2: Run credential-free Compose safety checks**

Use the repository's existing Compose verification targets/scripts and confirm:

- BlackPearl setup publication remains bound to loopback;
- Plex and BlackPearl remain on isolated networks except the intentional NFS
  host path;
- Plex stays unprivileged and its media mount stays read-only;
- no token or pairing value appears in rendered Compose output.

- [ ] **Step 3: Rebuild only BlackPearl and capture container identity**

Record the Plex container ID before and after the BlackPearl-only build/recreate;
they must match. Do not recreate Plex, Prowlarr, or their volumes.

- [ ] **Step 4: Exercise partial restore against the saved mixed manifest**

With one direct-range origin returning a typed transient failure, verify:

```text
GET /healthz -> 200 {"status":"ok"}
GET /readyz  -> 200 {"status":"ready"}
```

Mount/list the NFS namespace and confirm every active path opens. Confirm the
setup status reports the exact active/saved/unavailable counts and the setup
repository still loads the byte-equivalent full manifest.

- [ ] **Step 5: Restore provider reachability and prove automatic healing**

Wait for the existing bounded exponential retry loop. Confirm the full manifest
appears through NFS without restarting Plex or editing the saved manifest, and
the setup page returns to non-degraded ready state.

- [ ] **Step 6: Record current-build Plex evidence honestly**

If the user is signed into the isolated Plex browser, Direct Play one healthy
logical file and seek to a cold offset; record the current image/commit and stop
playback. If sign-in is unavailable, record server-side NFS/random-read proof
and leave authenticated Plex Direct Play/seek explicitly pending.

- [ ] **Step 7: Update acceptance evidence and commit**

Record exact commands, timestamps, container IDs, counts, hashes/offsets where
applicable, CI URL, and unresolved Windows/Ubuntu/Plex-login gates.

```text
git add docs/acceptance-evidence.md docs/macos-torbox-poc.md
git commit -m "docs: record degraded restore acceptance"
```

- [ ] **Step 8: Push and verify hosted CI**

Push `main`, wait for the exact commit's workflow, and require every job to pass
before reporting the change as integrated. Do not infer Windows Docker Desktop
or human Ubuntu playback from CI.
