# Bounded next-episode prefetch design

## Outcome

When Plex first opens a TV episode, the immutable runtime catalog identifies the
next episode of the same show by canonical virtual-path order and asks an
optional media-source capability to stage a configurable number of chunks from
that next logical file. Movies, the last available episode, and media sources
without this capability remain unchanged.

## Boundaries

- Core owns episode ordering because it owns catalog metadata.
- `MediaSource` remains the minimum open/readiness contract. A separate narrow
  optional interface schedules prefetch, preserving persistent-source compatibility.
- Rolling cache owns background fetching, quota, de-duplication, lifecycle, and
  error counters.
- One immutable catalog runtime schedules at most once for each opened episode,
  even if NFS reopens the path for many reads.
- The next item must be an episode under the same `TV Shows/Show (Year)` root.
  Lexicographic canonical paths order seasons and zero-padded episode numbers.
- Scheduling never changes the success or bytes of the current foreground open.

## Policy

`BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS` accepts 0 through 256; zero disables the
feature. The TorBox profile initially stages 16 MiB as sixteen 1 MiB chunks.
These chunks share the same hard quota and provider validator as seek-aware
read-ahead, retain one-chunk foreground headroom, and stop rather than evicting
chunks already serving current playback.

## Acceptance

- Opening S01E01 schedules the configured prefix of S01E02 once.
- Repeated and concurrent opens do not schedule the next episode repeatedly.
- Ordering crosses a season boundary but never crosses to another show.
- Movies and final episodes schedule nothing.
- Background fetches stay within quota and errors remain non-fatal and observable.
- Live Plex playback of one episode produces initial chunks for its selected next
  episode without a complete local file.
