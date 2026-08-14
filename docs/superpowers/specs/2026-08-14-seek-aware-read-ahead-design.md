# Seek-aware rolling read-ahead design

## Outcome

After a successful foreground `ReadAt`, BlackPearl opportunistically fetches a
small configurable number of immediately following chunks. Plex still sees the
same immutable logical size and arbitrary-offset read contract. A non-sequential
read moves the read-ahead window to the new offset.

## Safety and priority

- Foreground reads retain the existing exact, blocking behavior.
- Read-ahead never changes returned bytes or errors.
- Existing and in-flight chunks are deduplicated through the shared pool.
- Background work uses the process lifecycle and the existing bounded fetch timeout.
- Opportunistic reservations may evict old unpinned chunks, protect the most
  recently demanded chunk, and keep at least one complete chunk of quota
  available for foreground demand.
- A quota of two chunks or smaller effectively disables background work.
- Read-ahead count is configurable from zero through 64; zero disables it.
- All provider runtimes continue to share one quota and in-flight map.

## Observability

Rolling stats distinguish scheduled read-ahead fetches and background errors
from demand misses. Completed fetches remain part of the common fetch count and
hard-quota accounting.

## Acceptance

- A read of chunk zero schedules the configured next chunks without another caller.
- A read at a distant offset schedules from that seek position, not the old cursor.
- A foreground request joins an in-flight read-ahead fetch instead of duplicating it.
- Read-ahead never exceeds the hard quota and never consumes the reserved
  foreground headroom.
- Disabled read-ahead preserves current behavior.
- Race tests, rolling eviction tests, and all Compose profiles remain green.
