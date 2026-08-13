# Rolling Range Cache Design

## Goal

Prove that Plex can continuously Direct Play and seek within a logical media
file whose complete bytes never exist in BlackPearl's image, mounts, cache, or
container writable layer. BlackPearl will expose the existing logical file over
PearlNFS, fetch exact missing ranges from an explicitly configured HTTP origin,
and retain at most a fixed byte quota of local chunks.

This is the first rolling-storage milestone. It validates the storage and
streaming boundary before adding discovery, Prowlarr, TorBox, Usenet, archive
mapping, prefetch, or production provider credentials.

## Acceptance target

The macOS Docker Desktop stack will use the existing legal H.264/AAC test video,
but the complete MP4 will exist only in a separate range-origin container. The
BlackPearl container will use the fixture-free runtime image, receive no bind or
volume containing the source, and run with:

- storage mode `rolling`;
- a 1 MiB hard cache quota;
- 256 KiB chunks;
- PearlNFS as the filesystem frontend; and
- one explicit `http-range` backing object.

The 3,417,699-byte logical object is larger than the quota. Acceptance requires:

1. BlackPearl becomes ready without importing the complete fixture.
2. The official Plex container scans the logical movie through PearlNFS.
3. Plex Web records `Direct play OK` and serves the original MP4 part.
4. The eight-second video plays from beginning to end without interruption.
5. Forward and backward seeks resume playback at the selected offsets.
6. Exact non-sequential ranges returned through Plex match the origin bytes.
7. Cache accounting never exceeds 1,048,576 bytes, including in-flight
   reservations and temporary chunk bytes.
8. A previously evicted chunk is fetched again when Plex or the acceptance
   client revisits that range.
9. Inspection of the BlackPearl image and running container finds no complete
   MP4 and no mount sourced from the origin container.
10. Persistent mode and all Milestone 1 checks remain green.

Client-observed playback remains a separate manual gate. Automated tests prove
the exact-byte, quota, eviction, and re-fetch behavior; they do not substitute
for Plex's per-client Direct Play decision.

## Non-goals

- Provider discovery or candidate ranking.
- Prowlarr, TorBox, Usenet, NZB, NNTP, PAR2, or RAR integration.
- Read-ahead, adaptive scheduling, next-episode prefetch, or bandwidth policy.
- Cross-restart preservation of active reads.
- Cache encryption, multi-node coordination, or distributed locking.
- Modifying the existing FUSE implementation.
- Mounting or inspecting any production Plex, media, download, or `*arr` path.

## Architecture

```text
Plex Web
   |
official Plex container
   |
read-only Docker NFS volume
   |
PearlNFS -> Catalog.Open -> RollingSource.Open -> rolling ReadHandle
                                              |
                                 +------------+-------------+
                                 |                          |
                           cached chunk                missing chunk
                                 |                          |
                                 +---- exact ReadAt --------+
                                                            |
                                                HTTP Range gateway
                                                            |
                                              range-origin container
```

PearlFS, PearlNFS, Plex integration, and `domain.ReadHandle` do not change.
They continue to see a seekable logical object with a stable size. The selected
storage policy remains entirely behind `core.MediaSource`.

The dependency direction remains adapter to service to gateway:

- `core.Catalog` owns catalog orchestration.
- `cache.RollingSource` owns local chunk policy and implements the media-source
  behavior consumed by `core.Catalog`.
- `cache.RollingSource` defines the narrow `RangeOpener` interface it needs.
- `gateway/httporigin` maps HTTP responses into `acquisition.RangeSource`; it
  contains no cache or selection policy.
- `cmd/blackpearl` selects persistent or rolling dependencies from typed
  configuration and wires them once.

## Provider-neutral source boundary

The acquisition package keeps the existing random-access source contract:

```go
type RangeSource interface {
    ReadAt(ctx context.Context, destination []byte, offset int64) (int, error)
    Size() int64
    Close() error
}
```

The rolling cache defines its consumer-owned opener:

```go
type RangeOpener interface {
    Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error)
    Ready(ctx context.Context) error
}
```

An opener receives only a validated provider-neutral `BackingRef`. It does not
receive a local path and does not promise a complete download. Future TorBox or
other authorized providers can satisfy this boundary without changing either
filesystem frontend.

## Explicit HTTP Range gateway

The first gateway is configured with one absolute HTTP or HTTPS base URL and a
fixed provider name, `http-range`. It URL-escapes `BackingRef.ObjectID` as one
path segment, preventing an object identifier from changing the configured
origin host or escaping its path prefix.

Opening an object performs a bounded metadata request and records:

- logical `Content-Length`;
- a strong `ETag` when supplied; and
- `Last-Modified` only as a weaker fallback validator.

Each `ReadAt` sends `Range: bytes=start-end`. A valid response must be HTTP 206,
must contain an exact `Content-Range`, and must return exactly the expected
number of bytes except for the normal final partial read. HTTP 200 for a range
request is rejected so an origin cannot silently force BlackPearl to download
the complete object. A changed validator, malformed range, short response, or
oversized response fails the read with contextual error information and no
cache publication.

The gateway uses the injected instrumented HTTP client, propagates context, has
bounded headers and response bodies, and never logs authorization headers or
configured credentials. The POC origin requires no credentials; future gateway
authentication remains gateway-owned.

## Catalog registration

Persistent mode retains `Catalog.ImportPOC` unchanged. Rolling mode does not
call an importer. At startup it:

1. constructs `BackingRef{Provider: "http-range", ObjectID: configuredObject}`;
2. opens the object through the configured gateway to obtain its logical size;
3. closes that metadata handle; and
4. registers the existing legal POC title and virtual path in SQLite with the
   remote backing reference and logical size.

The core service gains a small `RegisterPOC` operation that validates the
backing reference and size before the repository upsert. This keeps hard-coded
POC metadata out of wiring and the external gateway. General media registration
and discovery remain later milestones.

## Rolling chunk storage

`cache.RollingSource` uses fixed 262,144-byte chunks. The final chunk may be
shorter. It maps every logical read to one or more chunk-relative copies and
processes chunks one at a time, so a large NFS read does not pin the entire
request window.

Object directories are derived from SHA-256 of the canonical provider name and
object ID. Chunk filenames contain only their zero-based index. No file is
created with the logical media size, and no sparse representation of the whole
object exists.

```text
cache/
  rolling/
    <sha256(provider + NUL + objectID)>/
      0000000000000000.chunk
      0000000000000007.chunk
```

Chunk publication is atomic:

1. reserve the exact expected chunk length against the quota;
2. fetch into a temporary file within the same object directory;
3. verify exact length and source response invariants;
4. synchronize and rename the temporary file;
5. set read-only service permissions and synchronize the directory; and
6. convert reserved bytes to current bytes without increasing accounted usage.

Failure or cancellation removes the temporary file and releases its
reservation. Startup removes abandoned temporary files, scans valid chunk
files, reconstructs least-recently-used order from modification time, and
evicts oldest chunks if persisted usage exceeds the configured quota.

## Hard quota and eviction invariants

All cache state is protected by one policy mutex. The invariant is:

```text
current chunk bytes + reserved fetch bytes <= configured maximum bytes
```

This accounting includes temporary fetch bytes before they are published.
Before reserving a miss, the cache evicts least-recently-used unpinned chunks
until the reservation fits. If every candidate is pinned or reserved, the
request waits on a capacity notification and remains cancellable through its
context. It does not exceed the quota and does not evict a chunk being copied to
a caller.

A cache hit pins its chunk before releasing the policy mutex. A miss publishes
and pins the new chunk in the same critical transition. The pin is released
after bytes are copied into the caller's buffer. Closing a logical read handle
prevents new reads through that handle but does not delete shared cached chunks.

The source exposes a concurrency-safe stats snapshot for tests and diagnostics:

```go
type Stats struct {
    CurrentBytes   int64
    ReservedBytes  int64
    HighWaterBytes int64
    ChunkCount     int64
    Hits           uint64
    Misses         uint64
    Fetches        uint64
    Evictions      uint64
}
```

`HighWaterBytes` measures current plus reserved bytes and can never exceed the
configured maximum.

## Concurrent misses

Only one origin fetch may exist for a given object and chunk index. The first
miss creates a keyed fetch operation; later callers wait for it while retaining
independent cancellation. Fetch work uses the BlackPearl lifecycle context plus
a bounded provider timeout, so cancellation of one Plex request does not abort
bytes still needed by other waiters. A failed fetch is not cached; a later read
may retry cleanly.

Different chunks may fetch concurrently only when their combined reservations
fit within the hard quota. This naturally applies backpressure without assuming
the complete object or a fixed sequential access pattern.

## Read semantics

The rolling handle reports the catalog's logical size. It follows `io.ReaderAt`
semantics:

- negative offsets fail;
- zero-length destinations return `(0, nil)`;
- offsets at or beyond logical EOF return `(0, io.EOF)`;
- reads crossing logical EOF return the available byte count with `io.EOF`;
- successful interior reads fill the requested buffer exactly; and
- context cancellation is returned without publishing incomplete chunks.

Sequential playback, MP4 metadata probes, and backward or forward seeking are
therefore the same operation: exact logical reads at different offsets.

## Configuration and wiring

Rolling mode adds these typed settings:

```text
BLACKPEARL_CACHE_CHUNK_BYTES=262144
BLACKPEARL_RANGE_ORIGIN_URL=http://range-origin/
BLACKPEARL_RANGE_OBJECT_ID=blackpearl-poc.mp4
BLACKPEARL_RANGE_TIMEOUT=30s
```

In rolling mode, the maximum cache size, origin URL, and object ID are required;
the maximum must be at least one chunk. `BLACKPEARL_POC_SOURCE` must be empty so
the process cannot accidentally import a complete local fixture. Persistent
mode does not require origin settings and preserves its existing defaults.

Wiring selects exactly one media source:

- persistent: existing content-addressed `cache.Store` as importer and source;
- rolling: HTTP gateway plus `cache.RollingSource`, with remote POC registration.

The same selected source feeds either FUSE or NFS. This milestone exercises NFS
on macOS and leaves the current FUSE implementation untouched.

## Docker isolation

A separate Compose profile, `compose.rolling.yaml`, contains:

- `range-origin`: a small read-only HTTP image containing the generated fixture;
- `blackpearl`: the fixture-free runtime image with only its data volume;
- `plex`: the same pinned, unmodified official image and read-only NFS volume.

The origin port is not published to the host. BlackPearl reaches it over the
project network. BlackPearl receives no origin volume, no host media bind, and
no production path. Published BlackPearl, NFS, and Plex ports remain bound to
host loopback. Cleanup names only this Compose project and its named volumes.

The origin response is intentionally rate-limited above the fixture's average
bitrate but below loopback speed. This makes progressive retrieval observable
without causing Plex buffering and lets the acceptance sampler observe several
fetch-and-evict cycles.

## Error handling and observability

Errors are wrapped at each boundary: logical read, cache chunk, provider object,
and HTTP range. Secrets and response bodies are not included. Stable domain
errors distinguish not found, invalid range, cache pressure cancellation, and
provider inconsistency where callers need different filesystem mappings.

Structured logs record object ID hashes rather than raw provider URLs. Key
events are rolling source readiness, fetch failure, validator mismatch, and
quota pressure. Successful per-chunk reads are debug-level to avoid noisy
production logs. Manual OpenTelemetry spans cover origin range requests and
cache misses; hits do not create spans.

## Test strategy

All production behavior is developed with red-green-refactor cycles.

### HTTP gateway tests

`httptest.Server` cases cover exact interior ranges, the final partial range,
EOF, ignored Range headers, malformed `Content-Range`, changed validators,
short bodies, oversized bodies, context cancellation, encoded object IDs, and
unsupported provider names.

### Rolling cache tests

Table-driven and concurrent tests use an instrumented fake `RangeOpener` and
prove:

- cross-chunk and non-sequential `ReadAt` results are byte-exact;
- a logical object larger than available local storage is readable;
- current plus reserved bytes never exceed the quota;
- least-recently-used unpinned chunks are evicted;
- pinned chunks are not evicted;
- revisiting an evicted chunk increments provider fetches;
- same-chunk concurrent misses produce one provider request;
- different chunks apply capacity backpressure;
- failed or cancelled fetches leave no temporary files or reservations;
- restart scanning removes temporary files and trims over-quota chunks; and
- close, EOF, and invalid offset behavior matches the domain contract.

### Service and wiring tests

Core tests prove remote POC registration does not invoke the persistent importer.
Configuration tests cover mode-specific requirements and reject mixed local
fixture plus rolling-origin settings. Application tests prove the selected
gateway and rolling source are injected only in rolling mode while persistent
startup remains unchanged.

### Docker and Plex acceptance

Automated acceptance will:

1. start only the isolated rolling Compose project;
2. verify BlackPearl contains no MP4 and has no origin/source mount;
3. verify the origin object is larger than the rolling quota;
4. create or reuse only the isolated `BlackPearl Rolling POC` Plex library;
5. scan the logical movie;
6. compare multiple non-monotonic Plex-served ranges with the origin;
7. stream the entire original part while sampling on-disk cache usage;
8. assert sampled usage and internal unit-test high-water stay within 1 MiB;
9. request an evicted early range and verify a new origin request; and
10. leave final Direct Play and interactive seek confirmation to the user.

The full verification gate remains `go vet`, race tests, at least 80% coverage,
Compose isolation checks, persistent portable acceptance, rolling automated
acceptance, and `git diff --check`.

## Safety and future compatibility

The rolling origin is explicitly configured and cannot be selected from media
metadata supplied by Plex. The filesystem remains read-only. No cleanup command
accepts an arbitrary target. Existing named volumes and production paths are
never searched or modified.

The design deliberately keeps read-ahead outside the first implementation.
Once exact demand reads, quota enforcement, Direct Play, and seeking are proven,
seek-aware scheduling can wrap the same chunk fetch primitive. Authorized
TorBox or Usenet adapters can later implement the same opener/source boundary;
providers that expose archives instead of final media bytes will require their
own mapping layer before they qualify as a `RangeSource`.
