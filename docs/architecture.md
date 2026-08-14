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

Persistent mode is suitable for a home server where the cache can retain entire objects. Later integrity and quota policy can extend this implementation behind the existing media-source boundary.

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

The implementation enforces `published chunk bytes + reserved fetch bytes <= quota`, coalesces concurrent misses, atomically publishes verified chunks, restores valid chunks after restart, and evicts least-recently-used unpinned chunks. Browser-selected runtimes share one process-lifetime rolling pool, preventing competing recovery scans or independent quota ledgers on the same directory. Configurable seek-aware read-ahead opportunistically fetches the chunks after the most recent foreground range, protects that demanded chunk, and retains one chunk of quota headroom. A seek immediately relocates the window. When Plex opens an episode, the catalog may also schedule a configurable prefix of the next episode in the same show, including across season boundaries. That work is asynchronous, deduplicated per catalog generation, shares the hard quota, retains foreground headroom, and stops rather than evicting current cache data; it never requires the next complete file. Adaptive throughput-based scheduling remains later work. None of these policies change PearlFS, PearlNFS, or Plex integration; the same binary selects the policy from configuration.

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

## Direct Play target

The low-storage VPS path treats Plex Direct Play as a primary constraint. BlackPearl delivers exact container bytes and does not transcode. Codec/container compatibility remains a Plex client concern; a provider resolver should eventually prefer Direct Play-compatible candidates when metadata is reliable. Milestone 1's synthetic fixture is MP4 with H.264 video, AAC audio, `yuv420p`, and fast-start metadata.

## State and path safety

SQLite owns catalog metadata only. Cache bytes and the optional FUSE mount live in separate configured directories. The portable profile uses project-scoped Docker volumes and no host media bind. Existing Plex and media paths are never searched or modified.

## Extension roadmap

1. Add automatic movie and episode metadata/watchlist ingestion without coupling Plex metadata to provider locators.
2. Add adaptive throughput-based read scheduling. Seek-aware read-ahead is implemented.
3. Extend the implemented bounded next-episode prefix prefetch with playback-aware cancellation and prioritization.
4. Implement provider-backed persistent retention policy alongside rolling mode.
5. Add additional explicitly authorized search and range providers.

Each stage needs its own acceptance evidence. A generic interface alone is not evidence that a provider, rolling cache, or progressive stream works.

The first provider adapter is `torbox-torrent`. It maps an already-complete
`torrent-id:file-id` account object to a short-lived HTTPS CDN link, validates
its size, and exposes strict ranges without persisting the API token or URL.
The cached-only TorBox creation contract is implemented and proven against
mocked TLS endpoints, including atomic catalog publication and paired browser/API
wiring. A live authorized account mutation remains separate acceptance evidence.
