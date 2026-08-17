# Degraded Saved-Manifest Restore Design

## Decision context

BlackPearl persists one complete provider-neutral media manifest and recreates
an immutable range-backed NFS catalog from it at startup. Runtime preparation
currently probes every backing before publishing anything. That is the right
transactional rule for a new setup selection or an acquired item, but it makes
startup all-or-nothing: one temporarily unreachable origin can hide every
otherwise healthy TorBox or direct-range file from Plex.

The live macOS acceptance stack exposed this failure mode after a BlackPearl
restart. The saved 15-item manifest remained intact, all nine TorBox objects
were healthy, and several Archive files were healthy, but three Archive storage
hosts timed out. Full restore therefore never published an NFS catalog and
`/readyz` correctly remained `setup_required` even though most of the library
was playable.

The selected design adds a restore-only degraded mode. BlackPearl may publish a
validated reachable subset during startup recovery, while preserving the full
saved manifest unchanged and retrying until it can atomically restore the full
catalog. Normal setup and acquisition publication remain all-or-nothing.

## Goals

- Keep Plex usable when one or more saved remote backings are transiently
  unavailable.
- Preserve the full durable manifest and provider credential without silently
  deleting, rewriting, or replacing an unavailable item.
- Publish only files whose logical size, immutable validator, license, and
  provider metadata still satisfy the existing validation rules.
- Keep `/readyz` truthful: ready when at least one catalog snapshot is active,
  including a degraded snapshot; setup required when no snapshot is active.
- Report active, saved, and unavailable counts clearly in the setup API and UI.
- Heal automatically to the complete manifest without restarting Plex.
- Preserve the range-oriented `ReadAt` path, rolling and persistent cache
  modes, Direct Play, and arbitrary seeking.

## Non-goals

- Do not weaken manual `Apply` or `PublishAcquired`; both continue to validate,
  persist, and publish one complete manifest transactionally.
- Do not serve a path whose backing failed validation.
- Do not persist provider URLs, response bodies, errors, or temporary
  availability state.
- Do not treat authentication, missing objects, changed size or validator,
  invalid metadata, or lost licensing as transient availability.
- Do not mutate Plex, remove a Plex library item explicitly, or change the
  existing Plex container.
- Do not solve Windows or Ubuntu acceptance in this slice.

## Considered approaches

### 1. Keep all-or-nothing restore

This preserves the strongest namespace invariant but lets one provider outage
hide the entire library. It leaves a working cache and healthy providers
unused, so it is rejected for startup recovery.

### 2. Publish every saved path without probing it

This makes the namespace complete, but Plex receives files that may fail on
their first metadata or playback read. It also hides integrity and licensing
changes behind generic I/O errors. Rejected.

### 3. Publish a validated reachable subset and retain the full manifest

This is selected. It gives Plex a stable, truthful catalog, never claims an
unvalidated file is available, and retains enough durable state to recover the
complete library automatically.

## Restore-specific service boundary

The existing `RuntimeFactory` remains the only preparation boundary used by
manual setup and acquisition publication:

```go
type RuntimeFactory func(
    ctx context.Context,
    token string,
    manifest domain.SetupManifest,
) (core.CatalogService, error)
```

A separate consumer-owned boundary prepares startup recovery:

```go
type RestorePreparation struct {
    Runtime        core.CatalogService
    ActiveManifest domain.SetupManifest
    SavedItemCount int
    Degraded       bool
}

type RestoreRuntimeFactory func(
    ctx context.Context,
    token string,
    manifest domain.SetupManifest,
) (RestorePreparation, error)
```

`setup.Service` receives both factories. The restore factory is not callable
from `Apply`, `PublishAcquired`, or an HTTP handler. This keeps degraded
publication a startup-recovery capability rather than a general escape hatch.

The wiring layer shares a single per-item validator and catalog builder between
the two factories. The full factory stops on any item error. The restore
factory may omit only an item that produced an explicitly classified transient
availability error.

## Error classification

Restore decisions operate on typed causes rather than error strings.

Omittable transient failures include:

- provider connection timeout, temporary DNS failure, or connection reset;
- provider HTTP 429 or 5xx response;
- a temporary signed-link or metadata endpoint availability failure.

Fatal restore failures include:

- rejected or expired provider credentials;
- object not found or permanently removed;
- logical size or immutable validator mismatch;
- invalid provider metadata, object ID, media path, or catalog registration;
- missing or changed licensing requirements;
- unsupported provider or media format.

The provider gateways must preserve typed authentication, not-found, integrity,
and transient causes through the range opener. Unknown local/domain validation
errors fail closed. Unknown external transport failures may be wrapped as the
typed transient availability error at the gateway boundary.

If a fatal failure is present, the restore factory returns no preparation and
the service publishes nothing new. If every item is transiently unavailable,
the service also publishes nothing and keeps retrying.

## Startup and retry behavior

On the first successful restore attempt after process start:

1. Load the full saved token and manifest generation.
2. Validate each item in manifest order.
3. Build one immutable catalog containing only validated items.
4. If every item validates, atomically publish the full catalog and report
   normal ready state.
5. If at least one item validates and every omission is transient, atomically
   publish the partial catalog, report degraded ready state, and return a typed
   degraded/unavailable signal so the existing background retry loop continues.
6. If no item validates, leave the current catalog unchanged and retry.

A degraded catalog is stable. Later partial probes must not replace it with a
different or smaller subset, because repeated transient failures could make
Plex paths flap. Background attempts continue to prepare the complete saved
manifest; the service replaces the degraded snapshot only when all saved items
validate in one attempt. The complete replacement uses the existing atomic NFS
publisher, so Plex does not observe a mixed generation.

The persisted token and full manifest are never changed by restore. A process
crash therefore restarts from the same durable source of truth.

## Interaction with setup and acquisition

Manual `Apply` and `PublishAcquired` remain serialized by the existing
transition lock and continue to use full-manifest preparation. A successful
mutation replaces the durable manifest and active catalog together. A failed
mutation leaves both unchanged.

While the service is degraded, acquisition publication may continue to prepare
against the full durable manifest and can fail if an unrelated saved item is
still unavailable. This conservative first slice favors transactional safety
over allowing concurrent manifest changes during a partial outage. The worker
retains its durable job and normal retry behavior. Once the full restore heals,
publication resumes without losing the queued acquisition.

Published-media lookups used for Plex playback advancement read the active
manifest snapshot, because it describes files Plex can actually open.
Durable acquisition deduplication continues to read the saved manifest so an
unavailable item is not reacquired or duplicated merely because its provider is
temporarily offline. These two meanings remain separate and get tests at their
service boundaries.

## Status API and setup UI

The OpenAPI setup status adds:

```yaml
savedItemCount: integer
activeItemCount: integer
unavailableItemCount: integer
degraded: boolean
```

`selectedItems` continues to represent the active Plex-visible snapshot.
Counts expose no titles, object IDs, provider errors, or credentials. Existing
clients can ignore the additive fields.

Normal ready copy remains unchanged. Degraded state displays a clear warning,
for example:

> 12 of 15 files are available. BlackPearl is retrying the other 3
> automatically.

The page remains usable for Watchlist and setup controls. `/readyz` returns 200
whenever at least one immutable catalog is active, including degraded mode.
Liveness remains independent of provider availability.

## Concurrency and lifecycle

- `Restore`, `Apply`, and `PublishAcquired` remain serialized by
  `transitionMu`.
- Runtime preparation uses the process/service lifecycle context, never the
  setup HTTP request context, for long-lived cache misses and range reads.
- An active catalog snapshot remains immutable after publication.
- A partial restore never edits the shared saved manifest in place.
- Cache pools remain process-scoped and shared across runtime generations, so
  restore does not create competing quota managers or delete another
  generation's in-flight chunks.
- NFS file handles already issued against an older immutable catalog retain
  that catalog until the handle is released; replacement affects new lookups.

## TDD and verification plan

Service tests define the behavior before implementation:

- full restore publishes all saved items and returns success;
- partial restore publishes the reachable subset, reports degraded counts, and
  returns the retryable degraded signal;
- the repository still contains the byte-equivalent full manifest after a
  partial restore;
- zero reachable items publish nothing;
- authentication, missing-object, integrity, license, size, and invalid-path
  failures publish nothing;
- a later partial probe does not replace or shrink the first active snapshot;
- a later full probe atomically replaces the degraded snapshot;
- manual `Apply` and `PublishAcquired` remain all-or-nothing;
- active playback lookup and durable acquisition deduplication use their
  intentionally different manifest views;
- concurrent restore and setup transitions pass under the Go race detector.

Wiring and provider tests cover a mixed TorBox/Archive manifest, preserved
ordering, transient HTTP/DNS classification, and fatal validator mismatch.
Handler, OpenAPI, and React tests cover additive status fields and normal,
degraded, and setup-required copy.

The completed change must pass:

```text
go test -race -count=1 ./...
go vet ./...
golangci-lint run
cd web && bun run lint && bun run test && bun run build
docker compose config safety checks
```

Live macOS acceptance uses the isolated BlackPearl stack only:

1. Preserve the full saved mixed-provider manifest.
2. Simulate or reproduce one transient direct-range origin outage.
3. Rebuild only BlackPearl and confirm the Plex container ID is unchanged.
4. Confirm `/readyz` is 200 and NFS lists the healthy subset.
5. Confirm the setup page reports exact active/saved/unavailable counts.
6. Restore the origin and confirm the complete manifest reappears without a
   Plex restart or manual manifest edit.
7. After the user signs in to the isolated Plex browser, Direct Play and seek a
   healthy logical file from the current build.

Windows Docker Desktop and a human-visible Ubuntu playback run remain separate
portability acceptance gates and must not be inferred from macOS success.
