# Direct-Range Acquisition Fallback Design

## Decision context

BlackPearl can already expose a logical file through PearlCache and NFS without
storing the whole file. TorBox-backed movies Direct Play and seek in Plex, and
the Watchlist pilot policy already creates exactly one immutable `S01E01`
request. Live legal/open pilot tests failed later: Internet Archive torrents
had no usable peers, so TorBox stalled before BlackPearl received a readable
file.

The root cause is narrower than the filesystem. `RangeSource`, the rolling and
persistent caches, PearlNFS, and Plex already accept arbitrary reads. The
durable acquisition job and saved setup manifest still assume that every
selected object is a TorBox torrent file. A Watchlist-only HTTP exception would
duplicate job state and couple the observer to one website, so this design
generalizes the acquisition and backing boundaries instead.

The user delegated implementation decisions and explicitly approved continued
in-place work while away. The live Docker stack is tied to this checkout, so
this milestone will use the current clean `main` checkout rather than create a
second worktree.

## Considered approaches

### 1. Keep retrying torrents

This requires no code but leaves successful playback dependent on external
seeders. It cannot satisfy on-demand behavior reliably and does not improve the
provider architecture. Rejected.

### 2. Add a Watchlist-specific HTTP publishing escape hatch

The Watchlist worker could detect a stalled job, fetch an Archive URL, and write
directly to the setup manifest. This is initially smaller, but it bypasses the
durable acquisition state machine, duplicates publication and retry behavior,
and makes Watchlist ingestion responsible for provider logic. Rejected.

### 3. Add a durable `torrent | range` candidate union

The acquisition job persists either a torrent fingerprint or an immutable
provider-neutral range backing. A trusted Archive adapter supplies exact
licensed files, while the existing cache/NFS/Plex path reads them through the
same `RangeSource` interface. This is the selected approach because it fixes the
shared boundary and becomes the foundation for future authorized HTTP, WebDAV,
Usenet, or debrid providers.

## Scope

This milestone will:

- preserve current TorBox discovery, cached acquisition, and torrent fallback;
- persist the backing provider alongside every setup manifest item;
- route one catalog across multiple `RangeOpener` implementations;
- add a durable candidate selection union with `torrent` and `range` variants;
- add a trusted Internet Archive exact-file resolver that requires an explicit
  Creative Commons or public-domain license URL;
- prefer immediately range-readable files after cached torrents and before
  uncached torrents;
- publish a range candidate through the same atomic setup transaction;
- prove one legal `S01E01` logical MP4 through rolling cache, NFS, Plex scan,
  Direct Play, and seeking without storing the complete file.

This milestone will not add arbitrary user-entered URLs, scrape HTML, send
credentials to Archive, transcode media, infer later episodes, or authorize a
season/series request.

## Domain model

### Provider-aware media candidate

`domain.MediaCandidate` and `domain.SetupConfiguration` gain a validated
`Provider` field. `Candidate()` returns the same provider. Existing constructors
remain narrow compatibility wrappers that default to `torbox-torrent`; new
constructors accept a complete `domain.BackingRef`. Legacy persisted manifests
that omit the field are validated as `torbox-torrent`.

Object uniqueness becomes `(provider, object_id)`, not `object_id` alone. Plex
path uniqueness remains unchanged. The browser may receive a provider name, but
never receives a URL, token, signed link, torrent payload, or Archive download
host.

### Durable job selection union

`acquisition.JobSelection` becomes a validated tagged union:

- `torrent`: the current locator-free release fingerprint, including info hash;
- `range`: a `BackingRef`, safe media name, positive logical size, source title,
  and display-only indexer name.

Constructors are explicit:

```go
func NewTorrentJobSelection(release Release) (JobSelection, error)
func NewRangeJobSelection(candidate RangeCandidate) (JobSelection, error)
```

The existing `NewJobSelection` remains a torrent compatibility wrapper.
Variant-specific accessors return `(value, bool)` so callers cannot read a zero
torrent release from a range selection.

`RangeCandidate` contains no URL:

```go
type RangeCandidate struct {
    backing   domain.BackingRef
    name      string
    size      int64
    title     string
    indexer   string
}
```

Its object ID is a provider-owned opaque identifier. The Internet Archive
adapter uses base64url-encoded `identifier` and `filename` components separated
by `~`; it reconstructs a current trusted host from Archive metadata at open
time.

## Persistence

A new SQLite migration rebuilds `acquisition_job_candidates` with a canonical
`selection_key` and variant columns, copies every existing row as `torrent`,
and recreates the selected-row indexes. `acquisition_jobs` gains selected kind,
backing provider, backing object ID, and media name columns. Existing selected
jobs default to `torrent`; queued/terminal rows remain valid.

`selection_key` is `torrent:<normalized-info-hash>` for torrents and
`range:<provider>:<sha256-of-object-id>` for range candidates. It is used only
for uniqueness and never sent to a provider. The migration preserves all
existing jobs, attempts, leases, outcomes, created objects, and publication
records.

## Provider and service boundaries

### Composite range routing

`cache.RangeRouter` implements `cache.RangeOpener` and dispatches `Open` by
`BackingRef.Provider`. It has an immutable map, rejects duplicate/unknown
providers, and reports readiness for each registered provider. One rolling or
persistent pool therefore retains a single quota ledger across TorBox and
Archive objects.

The browser runtime registers:

- `torbox-torrent` -> existing authenticated TorBox gateway;
- `internet-archive-file` -> new credential-free Archive range gateway.

Every catalog item uses its persisted provider instead of a hard-coded TorBox
provider.

### Archive metadata gateway

The external gateway maps bounded JSON from `/metadata/{identifier}` into a
provider-neutral item snapshot. It validates:

- trusted `archive.org` metadata origin and trusted HTTPS download hosts;
- item identifier, directory, and server fields;
- explicit Creative Commons or public-domain license URL;
- safe relative file names, positive sizes, supported `.mp4`/`.mkv` extension,
  SHA-1 digest, and modification time;
- response size, redirects, cancellation, and sanitized errors.

The gateway never accepts an arbitrary URL. The range source resolves the
current Archive host from metadata, verifies HEAD length and validator, and
requires exact HTTP 206 `Content-Range` responses. Its download transport uses
HTTP/1.1: a live probe on 2026-08-14 returned a correct 206 over HTTP/1.1 while
the same Archive download host repeatedly stalled range GETs over HTTP/2.

The stable PearlCache validator is the metadata SHA-1 digest. ETag or
Last-Modified is still used as `If-Range` and checked on every response.

### Direct candidate resolver

A service layer combines the existing open-media search with Archive item
metadata. It applies the same exact movie/episode filename eligibility used by
TorBox publication, rejects trailer/sample/preview extras, and emits at most one
licensed range candidate per Archive item. Results are deterministic and
bounded.

For a queued job, the worker orders candidates as:

1. cached TorBox torrents in resolver rank order;
2. immediately readable direct-range candidates in resolver rank order;
3. uncached TorBox torrents in resolver rank order.

The five-candidate hard limit remains. When a direct candidate exists, one slot
is reserved for the best direct candidate so five uncached torrents cannot hide
the on-demand path.

### Worker transitions

The worker keeps its existing durable stages:

```text
queued -> selected -> preparing -> succeeded
```

For a range selection:

1. `selected`: open the backing, require matching size and nonempty validator;
2. attach the same backing as a non-owned `CreatedObject`;
3. `preparing`: reopen/inspect it and produce one 100% media candidate;
4. publish through `setup.Service.PublishAcquired`;
5. commit `succeeded` with the opaque object ID.

No external mutation or cleanup occurs for a direct range. If the source is
temporarily unavailable, normal provider retry applies. If metadata/license,
size, validator, or exact-file eligibility changes, the candidate advances as
missing or unplayable. TorBox-owned cleanup behavior remains unchanged.

`Providers` keeps the current torrent interfaces and adds an optional paired
`DirectResolver` plus `RangePreparer`. Either both are present or both are nil,
so existing unit callers and configurations retain torrent-only behavior.

## Atomic publication and restart behavior

`AcquiredMedia` gains a range constructor but still exposes only exact request
and validated media candidate to the setup service. Publication continues to:

1. load the active manifest/token generation;
2. append or replace the exact logical movie/episode;
3. prepare a complete immutable catalog generation;
4. verify every TorBox or Archive backing;
5. save the new manifest generation;
6. atomically replace the NFS namespace/catalog;
7. request exact Plex library refreshes.

On restart, the manifest recreates the same mixed-provider catalog. Archive
host changes are resolved from the stable opaque object ID; cached chunks stay
keyed by provider/object/sha1 and remain reusable when the content is unchanged.

## Security and failure handling

- Only provider adapters registered at process startup can open a backing.
- Archive object IDs contain encoded identifier/file components, never a URL.
- Redirects are limited to HTTPS `archive.org` or subdomains on port 443.
- Explicit license metadata is mandatory; missing/unknown rights produce no
  direct candidate.
- Metadata and range bodies are strictly bounded.
- Provider responses, URLs, hosts, tokens, and file digests are not returned by
  setup or job APIs.
- Range sources fail closed on length, content range, digest identity,
  ETag/Last-Modified, or provider mismatch.
- A direct source is never deleted. Existing exact TorBox object ownership and
  deletion rules remain unchanged.
- The setup API remains isolated from Plex and retains its pairing boundary.

## Tests and acceptance

TDD proceeds in four independently reviewable slices:

1. provider-aware setup manifests and mixed-provider range routing;
2. durable torrent/range selection migration and repository round trips;
3. Archive metadata, license, HTTP/1.1 range source, and direct resolver;
4. worker orchestration, runtime wiring, and live macOS acceptance.

Automated coverage must include legacy manifest/job recovery, malformed opaque
IDs, unknown providers, duplicate mixed-provider objects, range candidate
ordering, optional direct providers, cancellation, transient unavailability,
changed size/validator, unlicensed items, unsafe paths/hosts/redirects, exact
episode selection, restart, and no provider deletion for direct sources.

Live acceptance requires:

- a newly Watchlisted legally usable show creates only `S01E01`;
- the durable job selects or falls back to `internet-archive-file`;
- publication adds exactly one canonical TV episode and preserves all existing
  manifest items;
- NFS start, interior, and tail reads match the source;
- Plex scans the episode and reports Direct Play for compatible video/audio;
- a discontinuous seek succeeds;
- BlackPearl's rolling cache stays below its configured quota and never contains
  the complete 175,099,607-byte test file;
- BlackPearl-only restart restores the logical episode and identical range;
- the temporary Watchlist item is removed without changing the user's original
  Watchlist entries.

If the chosen legal file is not Direct Play-compatible, it proves the range
path but does not complete this milestone; select another explicitly licensed
file rather than adding transcoding.
