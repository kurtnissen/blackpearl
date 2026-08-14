# Direct-Range Acquisition Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a durable movie or exact-episode job publish an explicitly licensed, immutable HTTP range object when cached or uncached torrents cannot provide on-demand bytes.

**Architecture:** Persist a tagged `torrent | range` selection and a provider-aware media backing. Route all catalog reads through one immutable multi-provider router and one shared cache quota. Add an Internet Archive adapter that turns licensed item metadata into opaque exact-file backings, then let the existing job, atomic setup publication, NFS, and Plex paths consume them.

**Tech Stack:** Go 1.24+, SQLite/modernc, standard `net/http`, existing OpenTelemetry boundaries, testify, Docker Compose, PearlCache, PearlNFS, Plex Web.

## Global Constraints

- Preserve all current TorBox behavior, saved manifests, jobs, ownership, cleanup, and API compatibility.
- No arbitrary URL input; an Archive backing is only an opaque encoded identifier plus filename.
- Require an explicit Creative Commons or public-domain license URL before emitting a direct candidate.
- Use strict bounded HTTP metadata and exact HTTP 206 range reads; never download the complete file as preparation.
- Keep one process-lifetime rolling or persistent quota across every provider.
- Watchlist show intent remains exactly S01E01 and can never authorize a season or series.
- Direct-range objects are never deleted because BlackPearl did not create them.
- Tests follow RED -> GREEN -> REFACTOR and run with `-race` before each feature commit.
- Work in the current clean checkout because the user explicitly approved in-place changes and the live Docker stack is tied to it.

---

### Task 1: Provider-aware setup and publication domain

**Files:**
- Modify: `internal/domain/setup.go`
- Modify: `internal/domain/setup_test.go`
- Modify: `internal/acquisition/result.go`
- Modify: `internal/acquisition/result_test.go`
- Modify: `internal/service/setup/acquired_test.go`
- Modify: `internal/service/setup/service.go`
- Modify: `internal/repository/setup/repository_test.go`

**Interfaces:**
- Produces: `NewProviderMediaCandidate(backing domain.BackingRef, name string, size int64) (MediaCandidate, error)`
- Produces: `MediaCandidate.Backing() domain.BackingRef`
- Produces: `SetupConfiguration.Backing() domain.BackingRef`
- Produces: `NewRangeAcquiredMedia(request SearchRequest, candidate domain.MediaCandidate) (AcquiredMedia, error)`
- Preserves: `NewMediaCandidate(...)` as a `torbox-torrent` compatibility constructor.

- [ ] Add failing table tests proving a provider-aware candidate round-trips through movie, episode, manifest, repository restart, and `Candidate()`, while an empty legacy JSON provider becomes `torbox-torrent`.

```go
backing, err := domain.NewBackingRef("internet-archive-file", "aWRlbnRpZmllcg~ZmlsZS5tcDQ")
require.NoError(t, err)
candidate, err := domain.NewProviderMediaCandidate(backing, "Show.S01E01.mp4", 175099607)
require.NoError(t, err)
configuration, err := domain.NewSetupEpisodeConfiguration(candidate, "Show", 2006, 1, 1, "Episode 1")
require.NoError(t, err)
require.Equal(t, backing, configuration.Backing())
```

- [ ] Run `go test ./internal/domain ./internal/repository/setup ./internal/service/setup -run 'Provider|Legacy|RangeAcquired' -count=1` and confirm failures are caused by absent provider-aware constructors/fields.
- [ ] Add `Provider string json:"provider,omitempty"` to candidate/configuration, validate it with `NewBackingRef`, default only an absent provider to `torbox-torrent`, key manifest duplicates by provider plus object ID, and make setup runtime configuration derive a backing through `Backing()`.
- [ ] Add `NewRangeAcquiredMedia`; keep `NewAcquiredMedia` torrent validation unchanged and make both constructors expose the same validated request/candidate publication surface.
- [ ] Run `go test -race ./internal/domain ./internal/acquisition ./internal/repository/setup ./internal/service/setup ./internal/handler/setup -count=1`.
- [ ] Commit `feat: persist provider-aware media backings`.

### Task 2: Mixed-provider range router and runtime factory

**Files:**
- Create: `internal/cache/range_router.go`
- Create: `internal/cache/range_router_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`
- Modify: `internal/core/catalog_test.go`

**Interfaces:**
- Consumes: provider-aware `SetupConfiguration.Backing()` from Task 1.
- Produces: `NewRangeRouter(openers map[string]RangeOpener) (*RangeRouter, error)`.
- Produces: `RangeRouter.Open(context.Context, domain.BackingRef) (acquisition.RangeSource, error)` and `Ready(context.Context) error`.

- [ ] Add failing tests proving routing by provider, unknown-provider rejection before I/O, constructor map copy, nil/duplicate invalidity, deterministic readiness, cancellation, and one rolling-pool quota shared by two routed providers.

```go
router, err := cache.NewRangeRouter(map[string]cache.RangeOpener{
    "torbox-torrent":        torboxOpener,
    "internet-archive-file": archiveOpener,
})
require.NoError(t, err)
opened, err := router.Open(ctx, domain.BackingRef{Provider: "internet-archive-file", ObjectID: "opaque"})
require.NoError(t, err)
require.Equal(t, 1, archiveOpener.opens)
require.Zero(t, torboxOpener.opens)
```

- [ ] Run `go test ./internal/cache -run RangeRouter -count=1` and observe RED.
- [ ] Implement an immutable sorted-key router with contextual errors that do not include object IDs.
- [ ] Change the browser runtime factory to build backings from configuration rather than hard-code `torbox-torrent`; initially register only TorBox so behavior remains unchanged until Task 5 wires Archive.
- [ ] Run `go test -race ./internal/cache ./cmd/blackpearl ./internal/core -count=1`.
- [ ] Commit `feat: route mixed range providers`.

### Task 3: Durable torrent/range candidate union

**Files:**
- Modify: `internal/acquisition/job.go`
- Modify: `internal/acquisition/job_test.go`
- Create: `internal/repository/acquisitionjob/migrations/003_range_candidates.sql`
- Modify: `internal/repository/acquisitionjob/repository.go`
- Modify: `internal/repository/acquisitionjob/repository_test.go`

**Interfaces:**
- Produces: `SelectionKind` values `torrent` and `range`.
- Produces: `RangeCandidate` plus `NewRangeCandidate(domain.MediaCandidate, string) (RangeCandidate, error)`.
- Produces: `RangeCandidate.Media() domain.MediaCandidate` and `RangeCandidate.Indexer() string`.
- Produces: `NewTorrentJobSelection(Release)` and `NewRangeJobSelection(RangeCandidate)`.
- Produces: `JobSelection.Kind()`, `TorrentRelease() (Release, bool)`, and `RangeCandidate() (RangeCandidate, bool)`.
- Preserves: `NewJobSelection(release)` as a torrent wrapper.

- [ ] Add failing domain tests for both selection variants, invalid cross-variant fields, stable selection identity, candidate-plan uniqueness, snapshot stage validation, and variant-safe accessors.
- [ ] Run `go test ./internal/acquisition -run 'JobSelection|RangeCandidate|JobSnapshot' -count=1` and confirm RED.
- [ ] Implement the tagged union. Store range identity as provider/object ID/name/size/indexer with no URL; keep torrent reconstruction locator-free.
- [ ] Add failing repository migration tests that open a v2 fixture containing queued, selected, preparing, succeeded, and candidate-fallback jobs, migrate it, and prove snapshots/candidates remain byte-for-byte equivalent at the public domain level.
- [ ] Add failing repository round-trip tests for a two-entry plan containing one torrent and one range selection, advancement in both directions, restart, stale claims, success, and concurrent one-winner claims.
- [ ] Run `go test ./internal/repository/acquisitionjob -run 'Migration|Range|Mixed' -count=1` and confirm RED.
- [ ] Add `selection_kind` and provider-neutral `selection_identity` columns. Rebuild the candidate table so uniqueness is `(job_id, selection_kind, provider, selection_identity)`, copy legacy rows as torrents, and update every query/transition/scan to reconstruct the union.
- [ ] Run `go test -race ./internal/acquisition ./internal/repository/acquisitionjob -count=1`.
- [ ] Commit `feat: persist direct range acquisition candidates`.

### Task 4: Trusted Internet Archive exact-file range gateway

**Files:**
- Modify: `internal/gateway/internetarchive/gateway.go`
- Create: `internal/gateway/internetarchive/files.go`
- Create: `internal/gateway/internetarchive/files_test.go`
- Create: `internal/gateway/internetarchive/source.go`
- Create: `internal/gateway/internetarchive/source_test.go`
- Modify: `internal/gateway/internetarchive/redirect_policy_test.go`

**Interfaces:**
- Produces: `const FileProviderName = "internet-archive-file"`.
- Produces: `Gateway.ListRangeCandidates(context.Context, acquisition.Release) ([]acquisition.RangeCandidate, error)`.
- Produces: `Gateway.Open(context.Context, domain.BackingRef) (acquisition.RangeSource, error)`.
- Preserves: `Gateway.Search`, `Materialize`, `Ready`, and torrent provider `Name()`.

- [ ] Add failing TLS contract tests for bounded metadata, explicit CC/public-domain license acceptance, unknown/missing license rejection, safe identifier/server/dir/file validation, supported original/derivative MP4/MKV files, deterministic opaque IDs, and credential/URL redaction.
- [ ] Add failing range tests for HEAD size/validator, HTTP/1.1-only download transport, exact non-sequential 206 reads, `If-Range`, changed validator/size, wrong `Content-Range`, redirects outside trusted Archive HTTPS hosts, cancellation, close, and EOF.

```go
backing := candidates[0].Media().Backing()
source, err := gateway.Open(ctx, backing)
require.NoError(t, err)
buffer := make([]byte, 16)
n, err := source.ReadAt(ctx, buffer, 1048576)
require.NoError(t, err)
require.Equal(t, 16, n)
```

- [ ] Run `go test ./internal/gateway/internetarchive -run 'File|Range|License' -count=1` and confirm RED.
- [ ] Implement base64url `identifier~filename` IDs, metadata SHA-1 cache validators, strict trusted-host resolution, and a cloned download transport with HTTP/2 disabled while leaving the caller's client untouched.
- [ ] Run `go test -race ./internal/gateway/internetarchive -count=1`.
- [ ] Commit `feat: read licensed Archive files by range`.

### Task 5: Exact direct candidate resolver and generic range preparer

**Files:**
- Create: `internal/service/directrange/resolver.go`
- Create: `internal/service/directrange/resolver_test.go`
- Create: `internal/service/directrange/preparer.go`
- Create: `internal/service/directrange/preparer_test.go`

**Interfaces:**
- Resolver consumes an interface with `Search` and `ListRangeCandidates`.
- Produces: `Resolver.Resolve(context.Context, acquisition.SearchRequest) ([]acquisition.RangeCandidate, error)`.
- Preparer consumes `cache.RangeOpener`.
- Produces: `Preparer.Prepare(context.Context, acquisition.RangeCandidate) (acquisition.CreatedObject, error)`.
- Produces: `Preparer.Inspect(context.Context, acquisition.JobSelection, acquisition.CreatedObject) (acquisition.PreparationInspection, error)`.

- [ ] Add failing resolver tests proving exact movie/year and S01E01 selection, trailer/sample rejection, one candidate per item, stable ranking, item-level partial failure, total failure, cancellation, and maximum result bounds.
- [ ] Add failing preparer tests proving metadata-only open, size/validator verification, non-owned created backing, restart-safe inspection, changed object/size failure, and no content read during preparation.
- [ ] Run `go test ./internal/service/directrange -count=1` and observe RED.
- [ ] Implement the resolver using the same public `acquisitionservice.SelectCandidate` eligibility function as TorBox publication. Implement the preparer as a provider-neutral `RangeOpener` consumer with contextual sanitized errors.
- [ ] Run `go test -race ./internal/service/directrange ./internal/service/acquisition -count=1`.
- [ ] Commit `feat: resolve exact direct range media`.

### Task 6: Durable worker ordering and variant dispatch

**Files:**
- Modify: `internal/service/acquisitionjob/worker.go`
- Modify: `internal/service/acquisitionjob/worker_test.go`
- Modify: `internal/service/setup/acquired_test.go`

**Interfaces:**
- Adds optional `Providers.DirectResolver` and `Providers.RangePreparer` as a required pair.
- Preserves mandatory `Searcher`, `Materializer`, and TorBox `Preparer`.
- Consumes: selection union and direct resolver/preparer from Tasks 3 and 5.

- [ ] Add failing worker tests for `cached torrent -> direct range -> uncached torrent` ordering, direct-slot reservation under the five-candidate cap, torrent-only compatibility, half-configured direct provider rejection, range prepare/inspect/publish/succeed, transient retry, advancement on missing/unplayable range, no direct deletion, restart, and exact episode publication.
- [ ] Run `go test ./internal/service/acquisitionjob -run 'Range|CandidateOrder|Direct' -count=1` and confirm RED.
- [ ] Refactor queued planning to build `JobSelection` values, dispatch selected/preparing work by `SelectionKind`, and construct `NewRangeAcquiredMedia` for direct publication. Keep all existing TorBox branches and cleanup calls unchanged.
- [ ] Run `go test -race ./internal/service/acquisitionjob ./internal/repository/acquisitionjob ./internal/service/setup -count=1`.
- [ ] Commit `feat: publish durable direct range candidates`.

### Task 7: Application wiring, full gates, and live legal S01E01 acceptance

**Files:**
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/macos-torbox-poc.md`
- Modify: `docs/acceptance-evidence.md`
- Modify: `docs/superpowers/plans/2026-08-14-direct-range-acquisition.md`

**Interfaces:**
- Runtime router registers `torbox-torrent` and `internet-archive-file`.
- Background worker receives the Archive direct resolver and generic range preparer whenever open-media search is enabled.

- [ ] Add failing app wiring tests proving a saved mixed-provider manifest restores, both providers share one cache pool, TorBox token is never passed to Archive, direct resolver is absent when open-media search is disabled, and a legacy TorBox-only manifest remains unchanged.
- [ ] Run `go test ./cmd/blackpearl -run 'Archive|MixedProvider|LegacyManifest' -count=1` and confirm RED.
- [ ] Wire one Archive gateway into both the range router and direct resolver, use `SetupConfiguration.Backing()` for every runtime item, and inject the direct provider pair into the acquisition worker.
- [ ] Run `make verify`, the pinned golangci-lint and govulncheck commands documented by the repository, `cd web && bun run lint && bun run test && bun run build && bun audit --production`, and all four Compose safety suites.
- [ ] Rebuild the TorBox Compose profile with the existing private volumes and one-minute Watchlist polling. Confirm setup status restores the eight-item manifest and both Watchlist controls without credential re-entry.
- [ ] Add only the legal MariposaHD show to Plex Watchlist, wait for exactly one S01E01 job, and record the selection kind/state without printing provider URLs, tokens, object IDs, or file digests.
- [ ] Confirm one canonical episode appears in `/blackpearl/TV Shows/MariposaHD (2006)/Season 01`, while the prior eight manifest items and original three Watchlist movies remain unchanged.
- [ ] Read 64 KiB at start, interior, and tail through the Plex NFS mount and compare to direct source ranges; record only pass/fail and hashes already safe for acceptance evidence.
- [ ] In signed-in Brave, scan/open the episode, verify Plex reports `directplay` with no `TranscodeSession`, perform one discontinuous seek, pause playback, and leave the working library tab open.
- [ ] Prove rolling cache bytes remain below quota and below the complete 175,099,607-byte file size, restart only BlackPearl, and repeat one identical NFS range read plus Plex resume.
- [ ] Remove the temporary MariposaHD Watchlist item and confirm the original three items remain. Do not remove the successfully published episode.
- [ ] Update documentation with exact evidence and explicit remaining Windows/native-Linux gaps, run `git diff --check`, and commit `docs: record direct range S01E01 acceptance`.
- [ ] Re-read the OG architecture roadmap and choose the next highest-leverage incomplete milestone; keep the active project goal open unless the full on-demand platform is genuinely complete.
