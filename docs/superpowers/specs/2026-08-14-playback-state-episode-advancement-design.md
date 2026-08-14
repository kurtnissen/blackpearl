# Playback-state episode advancement design

## Outcome

When an opted-in Plex Watchlist show has a successfully published BlackPearl
episode and real playback crosses a conservative progress threshold,
BlackPearl advances that show's durable acquisition frontier to exactly the
next episode. The next episode then uses the existing provider-neutral durable
job, range source, rolling cache, NFS publication, Plex refresh, and Direct
Play paths. No season or series request is ever created.

The initial threshold requires both 120 seconds watched and 10 percent of the
episode duration. A paused session still counts after reaching that threshold;
opening an item without watching it does not. The feature runs only while
automatic Watchlist acquisition and the `pilot` show policy are both enabled.

## Approaches considered

### Plex session and metadata APIs (selected)

Poll the isolated local Plex server's bounded `/status/sessions` response using
the existing read-only Plex credential source. A validated episode session
provides the exact media path, global Plex show GUID, season and episode,
duration, view offset, and player state. Resolve the next exact coordinate from
the bounded Plex metadata-provider hierarchy.

This is explicit playback evidence, supports season transitions, works with
unmodified Plex clients, and does not depend on a complete local media file.

### Plex database inspection (rejected)

Reading Plex's internal SQLite database could expose view offsets, but couples
BlackPearl to private schema and write-lock behavior. The existing config-volume
mount is intentionally a narrow credential boundary, not a general database
integration.

### NFS read inference (rejected)

Filesystem reads are portable but cannot safely distinguish user playback from
Plex analysis, thumbnail generation, probes, or arbitrary seeks. They are
useful for cache scheduling, not authorization to acquire another episode.

## Boundaries

### Playback gateway

`internal/gateway/plexplayback` owns the local Plex HTTP contract. It:

- accepts an absolute credential-free local Plex URL, the existing token source,
  the exact `/blackpearl` library root, and a bounded HTTP client;
- sends the token only as `X-Plex-Token`, refuses redirects, limits the response
  to 2 MiB and 64 sessions, and returns sanitized errors;
- accepts only `episode` sessions with `playing` or `paused` player state,
  positive bounded duration and offset, canonical season/episode coordinates,
  a `plex://show/<24 lowercase hex>` show GUID, and exactly one selected part
  under `/blackpearl/TV Shows/`;
- strips only the configured library-root prefix and returns a provider-neutral
  virtual path. It never returns a token, session identifier, client address,
  or playback URL.

`internal/gateway/plexmetadata` owns the Plex metadata-provider contract. Given
a validated show GUID and current coordinates, it reads a bounded season list,
then only the minimum required season episode lists. It sorts validated
coordinates and returns the smallest episode strictly after the current one.
This handles gaps and season transitions while ignoring specials at season 0.
No successor is a normal not-found outcome.

### Published-media index

The setup service adds a narrow exact-path lookup. It compares a session's
virtual path against paths derived from the active validated manifest and
returns only the matching episode configuration. This proves the session is
reading a BlackPearl-published object and prevents an identically named item in
another Plex library from authorizing work.

### Durable show frontier

The existing Watchlist show row becomes a serialized episode frontier after
S01E01 succeeds. A repository transition advances it only when every condition
matches atomically:

- the row is an auto-eligible show in `succeeded` state;
- automatic acquisition is enabled and show policy is `pilot`;
- the Watchlist item was observed within two configured Watchlist poll periods;
- source and external ID match `plex-watchlist` and the session's show GUID;
- the published object ID and current season/episode match the active manifest
  and playback session;
- the proposed next coordinates are strictly later and were returned by the
  metadata resolver.

The transition changes the same row to `pending`, stores the next exact
coordinates, clears only the previous job/publication attachment, resets its
attempt count, and leaves already published media in the manifest. The existing
Watchlist worker then submits or deduplicates one exact episode job. A repeated
or concurrent playback snapshot updates zero rows. A failed next episode stays
at that exact frontier and uses existing cooldown/retry behavior; playback of
the prior episode cannot skip past it.

Removing the show from Plex Watchlist stops further advancement after the
freshness window. It does not delete published media or cancel a provider
operation already committed under the prior policy.

## Playback worker

`internal/service/playbackadvance` coordinates the three narrow boundaries:

1. Read one bounded playback snapshot.
2. Ignore sessions below 120 seconds or 10 percent progress.
3. Resolve the session path in the active manifest and require matching
   season/episode metadata.
4. Ask the Watchlist repository whether that exact show/object is currently
   advancable and fresh.
5. Resolve one next episode through Plex metadata.
6. Commit the optimistic frontier transition.

The worker is process-serialized, polls every 30 seconds, uses a bounded
operation timeout, treats provider and Plex failures as non-fatal retries, and
records only sanitized structured errors. It performs no acquisition or
publication itself.

## Configuration and portability

The feature requires Watchlist observation and uses the same project-scoped
read-only Plex token source. New settings are:

- `BLACKPEARL_PLAYBACK_ADVANCEMENT_ENABLED` (default `false`);
- `BLACKPEARL_PLAYBACK_POLL_INTERVAL` (default `30s`);
- `BLACKPEARL_PLAYBACK_OPERATION_TIMEOUT` (default `15s`);
- `BLACKPEARL_PLAYBACK_METADATA_URL` (default
  `https://metadata.provider.plex.tv`).

The local Plex endpoint is the existing credential-free
`BLACKPEARL_PLEX_REFRESH_URL`. The portable TorBox Compose profile enables the
worker because its Plex server is isolated and reachable through the existing
Docker Desktop host-gateway path. The policy still defaults to off in durable
Watchlist settings, so enabling the process does not authorize acquisition.

The architecture works on Docker Desktop for macOS and Windows and on native
Linux. It requires no bind-mount propagation, custom Plex client, webhook,
filesystem completion, or direct container network shared with Plex.

## Error and safety behavior

- Unauthorized Plex responses are distinguished from temporary unavailability
  without returning credential material.
- Malformed or oversized session/metadata responses fail the poll closed.
- One malformed session is isolated; structurally invalid envelope counts fail
  the complete snapshot.
- A metadata change that removes the successor causes no frontier transition.
- Repository transitions are optimistic and idempotent across concurrent polls
  and restarts.
- Exact manifest capacity, provider selection, candidate fallback, range
  validators, ownership cleanup, and publication atomicity remain owned by the
  existing acquisition and setup services.
- BlackPearl never edits Plex Watchlist, Plex metadata, or existing library
  paths as part of the runtime feature.

## Acceptance

Automated acceptance must prove:

- below-threshold playback never advances;
- a qualifying S01E01 session creates only S01E02 intent;
- repeated and concurrent snapshots create one transition and one job;
- the next coordinate crosses a season boundary and skips season 0;
- unrelated roots, movies, malformed paths, mismatched manifest objects,
  disabled policy, stale/removed Watchlist rows, and terminal shows do nothing;
- a restart between frontier transition and job submission resumes safely;
- provider failures retry the same episode and never authorize a later one;
- all gateway bounds, redirects, authentication failures, cancellation, and
  sanitized error paths pass under `go test -race`.

Live macOS acceptance must temporarily add a legally usable show to the isolated
Plex Watchlist, preserve the pre-test Watchlist snapshot, and prove this chain:

1. S01E01 deduplicates against the already published BlackPearl episode.
2. Brave playback progress authorizes exactly one S01E02 frontier transition.
3. The existing direct-range path publishes a logical S01E02 without a complete
   local file.
4. Plex scans it, Direct Plays it, seeks in both directions, and keeps working
   after a BlackPearl-only restart.
5. The temporary Watchlist item is removed and the original snapshot is
   restored. Published test episodes are retained as explicit acceptance media.

If the authorized providers cannot produce S01E02, the frontier and job must
remain durable and exact; that is provider evidence, not permission to weaken
the one-episode invariant.
