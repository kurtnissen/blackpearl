# Provider-backed persistent retention design

## Outcome

The same browser-first BlackPearl binary can run its authorized TorBox-backed
library in either storage mode:

- `rolling` keeps a hard byte quota and evicts unpinned chunks; and
- `persistent` retains every verified range it has fetched and never evicts it.

Both modes continue to expose the same logical, seekable Plex files before the
complete media exists locally. Persistent mode therefore does not reintroduce a
download-before-playback requirement. A full sequential play gradually leaves
the complete object retained as independently verified chunks, while an early
stop leaves only the ranges that were actually requested or read ahead.

## Design choice

Extend the proven fixed-chunk range cache with an explicit retention policy
instead of creating a second download path or converting provider media into a
local-file-only catalog entry. The cache engine already owns exact range
validation, request coalescing, atomic chunk publication, restart recovery,
read-ahead, next-episode prefix prefetch, and immutable validator scoping.

The persistent constructor selects a separate `persistent` cache namespace and
an unbounded retention policy. Foreground and background reservations never
evict. The rolling constructor preserves its existing `rolling` namespace and
hard-quota behavior unchanged. Switching modes cannot accidentally mix the two
indexes or reinterpret an old quota.

No sparse file or completed-media marker is required. Chunk files remain the
durability unit, so a crash cannot advertise an unwritten range. Plex still
sees one ordinary logical file through PearlNFS; the chunk layout is private to
PearlCache.

## Browser runtime

Browser setup remains provider-oriented and constructs one process-lifetime
cache pool outside runtime replacement. In rolling mode it builds the existing
quota-bound pool. In persistent mode it builds the retained pool. Every
manifest generation and every issued NFS handle shares that one owner, so
replacement cannot race cache recovery or create competing indexes.

The TorBox Compose profile accepts `BLACKPEARL_STORAGE_MODE=persistent` as an
operator override while retaining `rolling` as the low-storage default. The
persistent path continues to use the same paired credential store, manifest
transaction, range gateway, NFS namespace, Plex refresh worker, Watchlist
observer, and acquisition coordinator.

## Configuration and safety

- Browser setup permits `persistent` or `rolling`, but still requires NFS and
  the `torbox-torrent` provider.
- Persistent browser mode requires a positive chunk size and fetch timeout.
- Persistent mode does not accept a hard cache quota; `CACHE_MAX_BYTES=0`
  explicitly means retain without eviction.
- Read-ahead and bounded next-episode prefetch are valid in provider-backed
  persistent mode.
- Legacy non-browser persistent fixture import stays unchanged and rejects
  provider settings.
- No credential, signed URL, provider locator, or production media path is
  added to cache filenames, logs, SQLite, or browser responses.

## Failure behavior

A failed range fetch leaves no published chunk. Existing retained chunks remain
readable across provider outages and restarts. A missing retained range still
requires the authorized provider, exactly like a rolling miss. Disk exhaustion
surfaces as a read error; BlackPearl does not silently evict persistent data or
fall back to rolling behavior.

## Acceptance

Automated tests must prove:

1. persistent range reads fetch exact arbitrary offsets and retain chunks;
2. a later read and a new process reuse retained chunks without provider I/O;
3. persistent retention grows beyond a size that would have forced rolling
   eviction, with zero evictions;
4. rolling hard-quota tests remain unchanged and green;
5. browser setup in persistent mode restores and replaces manifests through one
   shared pool; and
6. the TorBox Compose profile renders both default rolling and explicit
   persistent configurations without weakening network or path isolation.

Live macOS acceptance should switch the isolated profile to persistent mode,
read beginning/interior/tail ranges through NFS, restart BlackPearl, prove those
ranges are reused, then Direct Play and seek the known H.264/AAC item in Brave.
Windows Docker Desktop and native Linux remain separate evidence states.
