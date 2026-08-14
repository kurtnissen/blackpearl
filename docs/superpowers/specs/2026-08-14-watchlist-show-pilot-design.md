# Watchlist Show Pilot Design

## Goal

Make a TV show added to Plex Watchlist an explicit, bounded acquisition intent:
BlackPearl may prepare season 1 episode 1 and publish it into the existing Plex
TV library. It must never infer an entire season or series from a show-level
Watchlist record.

This is the next on-demand slice after automatic movies. It reuses the proven
durable acquisition job, TorBox preparation, rolling range cache, NFS catalog,
and Plex refresh paths.

## Approaches considered

### Recommended: explicit pilot-only policy

A paired setting enables `S01E01` for shows first observed after the setting is
enabled. The exact episode coordinates become immutable queue intent. Existing
shows remain observation-only. This is bounded, understandable, restart-safe,
and exercises the existing episode pipeline without bulk acquisition.

### Rejected for this slice: acquire a complete season

One Watchlist click could create dozens of provider mutations and large remote
downloads. It conflicts with the rolling-cache/VPS target and makes failures,
fallback, and ownership much harder to reason about.

### Deferred: infer the next unwatched episode

The Plex Discover Watchlist record does not provide a complete, authoritative
episode coordinate or local watched-state policy. A later playback-state slice
can request the next episode from an exact episode that Plex actually opened.
That will build on this feature's durable episode-intent representation.

## User-visible behavior

The paired Plex Watchlist panel retains the master automatic-adding control and
adds one independent option:

`Start new shows with S01E01`

- The option defaults off.
- The master automatic-adding setting must also be on.
- Enabling the option does not process shows already observed.
- A show first observed later becomes eligible for exactly season 1 episode 1.
- Disabling either setting blocks new claims and retries. It does not cancel an
  active provider operation, delete a job, remove media, or alter Plex Watchlist.
- The UI returns only aggregate counts and policy state. It never returns show
  titles, Plex identifiers, credentials, release locators, or provider objects.

## Durable model

The Watchlist settings singleton gains `show_policy`, constrained to `off` or
`pilot`. Existing databases migrate to `off`. The policy is durable and remains
authoritative across restart.

Each queue row gains immutable `intent_season` and `intent_episode` columns.
New observations are converted into a provider-neutral observation value:

- eligible movie: coordinates `0, 0`;
- eligible pilot: coordinates `1, 1`;
- observation-only movie or show: coordinates `0, 0` and not eligible.

Conflict updates refresh display metadata and observation timestamps only.
They never change eligibility or coordinates. A policy change therefore cannot
retroactively drain a historical Watchlist.

Claims return the persisted intent. A claim constructs a movie search for a
movie row or an episode search for a show row with positive persisted
coordinates. Invalid legacy or corrupted combinations fail validation and
never reach a provider.

## Runtime flow

1. The observer reads the durable policy before recording a provider snapshot.
2. The first successful process sync remains a non-acquiring baseline.
3. On later syncs, the observer marks new movies eligible under the master
   setting and new shows eligible only under master plus `pilot`.
4. The atomic claim query checks the current master setting and, for show rows,
   the current show policy before leasing work.
5. The Watchlist worker deduplicates the exact persisted intent against the
   active manifest.
6. If absent, it submits the existing durable acquisition job. That worker
   remains the sole owner of Prowlarr search, TorBox mutation, fallback,
   preparation, publication, and Plex refresh.
7. Published episode metadata uses the existing canonical TV path and range
   source. No complete local file is required.

## Interfaces

`WatchlistPolicy` is a provider-neutral value with `AcquisitionEnabled` and
`ShowPolicy`. The repository exposes `Policy(context.Context)` and
`SetPolicy(context.Context, WatchlistPolicy)`.

`WatchlistObservation` couples one validated Watchlist item to immutable
eligibility and episode coordinates. The observer creates observations; the
repository only persists them.

`WatchlistClaim.SearchRequest()` returns the exact durable movie or episode
intent. The worker does not invent episode coordinates.

The published-media boundary becomes intent-oriented:

```go
FindPublished(ctx context.Context, request acquisition.SearchRequest) (objectID string, found bool, err error)
```

It matches movies by title/year and episodes by show title/year/season/episode.

`PUT /api/watchlist/settings` accepts exact bounded JSON:

```json
{
  "acquisitionEnabled": true,
  "showPolicy": "pilot"
}
```

The existing status response adds `showPolicy` with enum `off|pilot`.

## Failure and safety behavior

- Policy and claim checks are atomic at the SQLite boundary.
- A disabled policy returns no claim; it does not mutate queued rows.
- Invalid persisted intent moves through the existing manual-review boundary
  rather than being guessed or sent to a provider.
- Provider errors retain the current retry/manual-review semantics.
- Policy writes use the existing paired loopback, exact Origin, CSRF,
  session/bootstrap, and bounded JSON controls.
- BlackPearl never writes to Plex Watchlist and never touches host media paths.

## Tests and acceptance

- Domain tests prove valid movie, observation-only show, and pilot intent plus
  rejection of mismatched coordinates.
- Repository race tests prove migration, policy persistence, immutable intent,
  disabled claim gating, and one-winner show claims.
- Observer tests prove baseline protection and dynamic show policy changes.
- Worker tests prove exact S01E01 submission, published-episode deduplication,
  restart reconciliation, and invalid-intent manual review.
- Handler/OpenAPI tests prove exact policy validation and paired mutation.
- React tests prove on/off behavior, pending/error rollback, and safety copy.
- Live macOS acceptance toggles the show policy, restarts BlackPearl, observes a
  newly added test show, and proves either exact published-episode deduplication
  or a legal provider-backed S01E01 publication without changing the user's
  pre-existing Plex Watchlist after cleanup.

## Explicit non-goals

- Whole-season or whole-series acquisition.
- Guessing a current or latest episode.
- Reading Plex playback history to infer watched state.
- Acquiring S01E02 automatically in this slice.
- Changing the existing range, cache, NFS, or Plex client contracts.
