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
5. Acquisition separates discovery from reading. A future opened `RangeSource` can satisfy arbitrary logical offsets without a full download.
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

The implementation enforces `published chunk bytes + reserved fetch bytes <= quota`, coalesces concurrent misses, atomically publishes verified chunks, restores valid chunks after restart, and evicts least-recently-used unpinned chunks. Read-ahead and adaptive scheduling remain later work. None of these policies change PearlFS, PearlNFS, or Plex integration; the same binary selects the policy from configuration.

## Direct Play target

The low-storage VPS path treats Plex Direct Play as a primary constraint. BlackPearl delivers exact container bytes and does not transcode. Codec/container compatibility remains a Plex client concern; a provider resolver should eventually prefer Direct Play-compatible candidates when metadata is reliable. Milestone 1's synthetic fixture is MP4 with H.264 video, AAC audio, `yuv420p`, and fast-start metadata.

## State and path safety

SQLite owns catalog metadata only. Cache bytes and the optional FUSE mount live in separate configured directories. The portable profile uses project-scoped Docker volumes and no host media bind. Existing Plex and media paths are never searched or modified.

## Extension roadmap

1. Implement provider-neutral resolver behavior and selection tests.
2. Add production authentication to an explicitly authorized ranged acquisition provider.
3. Add seek-aware read-ahead and adaptive scheduling.
4. Add bounded next-episode prefetch.
5. Add optional Prowlarr discovery and additional authorized providers.

Each stage needs its own acceptance evidence. A generic interface alone is not evidence that a provider, rolling cache, or progressive stream works.
