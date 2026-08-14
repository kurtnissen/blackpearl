# Architecture

## Decision summary

BlackPearl begins as one modular Go service. It keeps infrastructure adapters at the edge and uses narrow service-owned interfaces so future providers and cache policies do not leak into PearlFS or PearlNFS.

```text
FUSE adapter ----+
                 +--> Catalog service ----> Repository interface ----> SQLite
NFS adapter -----+           |
                             +----> MediaSource interface ----> PearlCache policy
                             |                               |          |
                             +-- ReadAt(ctx, buffer, offset) |          +-- rolling chunks
                                                             +-- persistent object
                                                                        |
                                                                RangeSource gateway

HTTP diagnostics -> Readiness service
Catalog service  -> Resolver interface -> acquisition provider contracts
Catalog service  -> Plex gateway (optional refresh only)
```

The dependency direction is adapter to service to repository/gateway. Core does not import FUSE, NFS, SQLite, HTTP framework details, or a named acquisition provider.

On-demand acquisition begins at a separate media-intent boundary. A validated
movie or episode request fans out to explicitly configured search gateways.
The first adapter performs read-only Prowlarr searches and maps ephemeral
torrent/NZB locators into immutable release values. Resolver policy tolerates a
partial provider outage, sanitizes all-provider failures, rejects malformed
results, deduplicates stable identities, and ranks complete intent matches
before provider-specific acquisition. Release locators are neither catalog
backing references nor persisted setup state.

Prowlarr may place its own HTTP download proxy in the response field named
`magnetUrl`. The adapter does not reinterpret that HTTP value as a magnet: it
discards only that mislabeled locator, retains the separately validated
download URL and info hash, and leaves every non-HTTP magnet value subject to
the normal strict magnet validator. This preserves live Prowlarr compatibility
without weakening release or SSRF boundaries.

The cached TorBox acquisition service consumes only ranked torrent releases
with stable hashes. It performs a read-only cache lookup, then creates exactly
one account object with TorBox's cached-only guard enabled. A bounded inspection
policy waits for that object to expose eligible video files, selects the exact
episode or best movie candidate, and hands a provider-neutral acquired-media
value to setup's existing durable manifest transaction. Provider creation is
not retried after an ambiguous response. The paired localhost API exposes this
transaction without accepting release locators from the browser: users submit
only validated movie or episode intent, and the backend owns ranking, cache
selection, creation, inspection, and publication.

An explicit cache miss may instead become a durable acquisition job. SQLite
stores the validated intent, up to five ordered provider/source fingerprints,
candidate outcomes, the selected ordinal, created provider object ID and
ownership provenance, lease/version state, and public error code. It never
stores credentials, magnets, download URLs, signed media URLs, or torrent-file
bytes. A serialized process-lifetime worker combines authorized search
providers, rejects results that do not satisfy the complete movie/episode
intent, rejects auxiliary-video markers outside the requested title,
deduplicates stable hashes, and probes up to 100 eligible torrent hashes before
moving TorBox-cached candidates ahead of uncached candidates. Only then is the
ordered plan capped to five durable fallbacks. Planning and first selection are
one SQLite transaction before provider mutation.

For each selected candidate, the worker reconciles by hash before mutation,
materializes bounded transient provider input when absent, records whether the
job created the exact account object, and polls only that object. Not-ready
state is deferred. A provider-reported incomplete stall, a disappeared object,
or completed content without playable media records the candidate outcome and
atomically selects the next release. BlackPearl deletes an abandoned object
only when the same job durably owns it; an ambiguous cleanup response is never
retried and enters manual review. Existing account objects and published
objects are never fallback-cleaned. Candidate exhaustion fails the job with a
public-safe code. Publication uses the existing manifest transaction and Plex
refresh boundary.

The optional `internet-archive` gateway is a legal-POC search provider, not a
filesystem source. It queries only the public Archive metadata endpoint,
returns stable item identifiers, structured movie years, and info hashes, and
materializes the selected item's bounded `.torrent` file. A movie result is
accepted only when its structured Archive year equals the requested year; that
year is appended to the normalized release title when the source title omits
it. Redirects are limited to the configured origin or official Archive HTTPS
hosts, and the bencoded info dictionary must hash to the selected fingerprint.
The durable worker combines Archive and Prowlarr results so a partial provider
outage does not discard the other source. Movie results must begin with the
complete requested title and include the requested year; episode results must
contain the complete title and `SnnEnn` token. Extra `trailer`, `teaser`,
`sample`, `preview`, or `featurette` tokens make either kind of release
ineligible, without rejecting a marker that is actually part of the requested
title. This prevents a cached auxiliary or unrelated title hit from bypassing
intent correctness. Both gateways feed the same provider-neutral release and
job contracts.

## Invariants established in Milestone 1

1. A catalog item stores a logical size and `BackingRef{Provider, ObjectID}`. It does not expose a local path.
2. `domain.ReadHandle` is context-aware and random-access:

   ```go
   type ReadHandle interface {
       ReadAt(context.Context, []byte, int64) (int, error)
       Size() int64
       Close() error
   }
   ```

3. PearlFS and PearlNFS forward Plex's offset reads to that handle. Neither inspects cache occupancy or requires a completed object.
4. The media-source boundary receives the complete catalog record, allowing a future policy to combine logical metadata, cached chunks, and a provider reference.
5. Acquisition separates discovery from reading. An opened `RangeSource` can satisfy arbitrary logical offsets without a full download and exposes an immutable version validator used to scope cached chunks.
6. The filesystem is immutable and reports the logical size to Plex, which allows metadata probes and seeking.

The test suite includes a one-terabyte logical source that generates only requested bytes and successfully serves a read at the end of the object. This is the regression test that prevents a hidden complete-file assumption from returning.

## Persistent mode

The current implementation imports the POC fixture into a SHA-256-addressed object. Publication is atomic: copy to a temporary object, synchronize it, rename it, set read-only service permissions, and synchronize the directory. Core stores only the resulting backing reference and size.

Provider-backed browser setup also uses persistent mode on a home server. It
retains every verified fixed-size range in a separate `persistent` namespace
without eviction. Missing ranges still come from the authorized `RangeSource`,
so first playback and arbitrary seeks begin before a complete local object
exists. Sequential playback gradually retains the requested object; restart
recovery reuses published chunks without refetching their ranges. The rolling
and persistent policies share validation, coalescing, atomic publication,
read-ahead, and prefetch behavior but never share a cache index.

## Rolling mode

Rolling mode divides a logical object into independently addressable chunks and:

- serve present ranges locally;
- fetch missing ranges from an authorized `RangeSource`;
- coalesce concurrent requests for the same range;
- preserve arbitrary seeks without requiring read-ahead;
- pin chunks held by active readers;
- evict unpinned chunks to stay within the configured 40-80 GB or other quota;
- verify range length and integrity before publication; and
- apply backpressure when all cache capacity is pinned or reserved.

The implementation enforces `published chunk bytes + reserved fetch bytes <= quota`, coalesces concurrent misses, atomically publishes verified chunks, restores valid chunks after restart, and evicts least-recently-used unpinned chunks. Browser-selected runtimes share one process-lifetime rolling pool, preventing competing recovery scans or independent quota ledgers on the same directory. Configurable seek-aware read-ahead opportunistically fetches the chunks after the most recent foreground range, protects that demanded chunk, and retains one chunk of quota headroom. Each open range handle owns a generation-tagged background window: a discontinuous read cancels stale requests before moving to the new offset, and close cancels the remaining window. A foreground reader that had joined canceled background work transparently retries under the process lifecycle. When Plex opens an episode, the catalog may also schedule a configurable prefix of the next episode in the same show, including across season boundaries. That work is asynchronous, deduplicated per catalog generation, shares the hard quota, retains foreground headroom, and stops rather than evicting current cache data; it never requires the next complete file. Adaptive throughput-based scheduling remains later work. None of these policies change PearlFS, PearlNFS, or Plex integration; the same binary selects the policy from configuration.

Browser setup persists one credential plus a validated manifest of 1-100 movie
or TV-episode selections. It prepares every provider object and a fresh in-memory catalog
before atomically replacing persistence and the NFS namespace. Object IDs and
Plex paths must be unique. Existing single-selection generations migrate in
memory to a one-item manifest, so upgrades do not invalidate saved credentials.

Movies publish under `Movies/Title (Year)/Title (Year).ext`. TV episodes publish
under `TV Shows/Show (Year)/Season NN/Show (Year) - SnnEnn - Episode.ext`.
Both filesystems materialize these directory trees from catalog paths and still
delegate all bytes to the same range-oriented read handle.

PearlNFS publishes each namespace together with the catalog that supplies its bytes. NFS file handles retain an immutable generation snapshot, while new lookups use the newest generation. This keeps active reads stable during browser-driven media replacement, including when a replacement reuses the same Plex path.

After that atomic publication succeeds, a separate process-lifetime worker
coalesces refresh signals and asks Plex to rescan every library whose root is
exactly `/blackpearl/Movies` or `/blackpearl/TV Shows`. The Plex gateway
discovers section keys at request time, authenticates only with a header, bounds
every response, and refuses redirects. Refresh retries are best-effort and
cannot roll back or block the published namespace. The isolated Compose profile
keeps Plex and BlackPearl on disjoint networks and reaches the host-published
Plex endpoint without exposing the control API to Plex.

The Watchlist observer writes provider-neutral movie or exact-episode intent to
its own SQLite queue. When explicitly enabled, a separate Watchlist worker
submits that intent to the durable acquisition-job manager and persists the
returned job ID before releasing its lease. Later claims only reconcile public
job state. Search, provider mutation, preparation polling, publication, and Plex
refresh remain owned by the acquisition-job worker. This allows a Watchlist
request to survive restarts and multi-hour preparation without holding a
Watchlist lease.

The durable policy has an independent master acquisition switch and show policy.
The only show policy implemented today is `pilot`: a show first observed after
both controls are enabled receives immutable coordinates `1,1`, which map to one
`S01E01` search request. `off` and all baseline shows retain coordinates `0,0`
and cannot be claimed. No Watchlist state can represent a season or series
request. The first successful observer sync after startup is a non-acquiring
baseline; immutable per-row eligibility permits only items first seen on later
opted-in syncs, so enabling the feature cannot drain a historical Watchlist
backlog. Observer syncs read the policy before assigning eligibility, and the
atomic claim statement checks it again before leasing work. Disabling therefore
blocks matching new and retry claims immediately without canceling an
already-running provider operation or changing the manifest. Advancing beyond
S01E01 requires a future explicit playback-state signal, not continued
Watchlist membership.

Provider readiness inspection returns safe media candidates together with a
provider-neutral integer progress value. TorBox maps its fractional progress to
0-99 while an object is incomplete and reports 100 only after the object is
complete and present. The durable worker commits the maximum of the saved and
latest values, so a stale provider poll cannot move one candidate backward.
The existing atomic fallback transition resets progress to zero for the next
candidate, and successful publication commits 100. Credentials, transient
locators, provider response bodies, speeds, and ETA values are never persisted.

Acquisition candidates are a tagged `torrent | range` union. Torrent candidates
retain their locator-free release fingerprint and ownership rules. Range
candidates retain only a validated provider name, opaque object identity,
logical filename, size, and resolver label. Planning orders cached torrents,
licensed direct ranges, then uncached torrents while reserving one of the five
durable slots for a direct source when one exists. Preparation of a range
candidate opens metadata only to validate size and immutable version; content
is fetched later through the same `ReadAt` and shared cache path used by every
filesystem frontend. The runtime's immutable range router dispatches each
backing by provider without exposing a URL to the catalog. A provider object
BlackPearl did not create is never eligible for cleanup.

## Direct Play target

The low-storage VPS path treats Plex Direct Play as a primary constraint. BlackPearl delivers exact container bytes and does not transcode. Codec/container compatibility remains a Plex client concern; a provider resolver should eventually prefer Direct Play-compatible candidates when metadata is reliable. Milestone 1's synthetic fixture is MP4 with H.264 video, AAC audio, `yuv420p`, and fast-start metadata.

## State and path safety

SQLite owns catalog metadata only. Cache bytes and the optional FUSE mount live in separate configured directories. The portable profile uses project-scoped Docker volumes and no host media bind. Existing Plex and media paths are never searched or modified.

## Extension roadmap

1. Add an explicit playback-state signal that can advance an opted-in show from the implemented S01E01 pilot to the next exact episode; Watchlist membership must never authorize a season or series.
2. Add a stable Plex Watchlist provider contract or RSS fallback.
3. Add adaptive throughput-based read scheduling. Seek-aware read-ahead is implemented.
4. Extend bounded next-episode prefix prefetch with the playback-state signal; ordinary read-ahead cancellation is handle-aware now.
5. Add disk-capacity observability and operator alerts for non-evicting persistent retention.
6. Add additional explicitly authorized search and range providers.

Each stage needs its own acceptance evidence. A generic interface alone is not evidence that a provider, rolling cache, or progressive stream works.

The first provider adapter is `torbox-torrent`. It maps an already-complete
`torrent-id:file-id` account object to a short-lived HTTPS CDN link, validates
its size, and exposes strict ranges without persisting the API token or URL.
The cached-only TorBox creation contract is implemented and proven against
mocked TLS endpoints, including atomic catalog publication and paired browser/API
wiring. A live authorized account mutation remains separate acceptance evidence.
