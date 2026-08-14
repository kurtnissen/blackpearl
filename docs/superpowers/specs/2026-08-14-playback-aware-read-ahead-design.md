# Playback-aware read-ahead design

## Outcome

Rolling and provider-backed persistent caches stop obsolete read-ahead work when
Plex seeks to a discontinuous offset or closes the logical file handle. Exact
foreground reads remain authoritative and retry independently if they had
joined a background fetch that was canceled.

This is handle-scoped scheduling, not Plex-client integration. It requires no
timeline API, custom player, codec inspection, or filesystem contract change.

## Handle lifecycle

Each range-cache handle owns one cancelable read-ahead window derived from the
process-lifetime cache context. The first successful foreground read opens the
window. Sequential reads extend that window. A discontinuous read cancels the
old window before fetching the new foreground offset, then starts a fresh
window after the demanded bytes succeed. `Close` cancels the active window.

The handle tracks a monotonically increasing generation. A concurrent read may
finish after another read moved the window; generation checks prevent that stale
completion from moving the active playback position backward.

## Shared fetch behavior

Chunk fetches remain globally coalesced across handles and catalog generations.
Each background fetch receives the window context that scheduled it. Foreground
fetches continue to use the process lifecycle context.

If a foreground read is waiting on a read-ahead fetch and that fetch is canceled
because its owner seeks or closes, the foreground read loops through cache
lookup again and performs or joins a foreground fetch. Cancellation therefore
removes obsolete work without surfacing a spurious playback error to another
reader.

Next-episode prefix prefetch keeps its existing process-lifetime context in this
slice. NFS clients can open and close handles for short metadata reads, so tying
episode prefetch directly to one file handle would cancel useful work too
aggressively. A future playback-state signal can refine that policy separately.

## Safety and compatibility

- No cache file, key, quota, validator, or recovery format changes.
- Rolling foreground headroom and hard quota remain unchanged.
- Persistent retention remains non-evicting.
- A canceled background temporary file is removed and its reservation released.
- Metrics continue to classify canceled obsolete work as a background error;
  a later observability slice may split cancellation from provider failure.
- Existing read-ahead, prefetch, random-read, replacement, and restart tests
  remain green under the race detector.

## Acceptance

Race tests prove a far seek cancels a blocked stale range, closing a handle
cancels its blocked read-ahead, a second foreground handle transparently retries
after shared background cancellation, sequential playback retains useful
read-ahead, and cache reservations return to zero without temporary-file leaks.

Live macOS acceptance plays the known Direct Play-compatible movie, observes
rolling background activity, performs a far seek, and confirms playback advances
from the new position while the cache remains within its configured quota.
