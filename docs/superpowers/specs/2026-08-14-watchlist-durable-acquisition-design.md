# Watchlist Durable Acquisition Design

## Goal

Make a movie added to the Plex Watchlist eligible for the same restart-safe
TorBox preparation path already proven by the paired setup console. BlackPearl
continues to read the Watchlist without modifying it. TV shows remain
observation-only until an explicit season and episode policy exists.

## Chosen architecture

Keep the Watchlist queue and acquisition-job queue as separate durable
components. A Watchlist worker owns only orchestration between them:

1. claim one eligible movie from the Watchlist queue;
2. submit or deduplicate a durable acquisition job;
3. persist the returned job ID on the Watchlist record before releasing its
   lease;
4. reconcile that job on later passes; and
5. copy only the terminal public outcome back to the Watchlist queue.

The acquisition-job worker remains the sole owner of search, release ranking,
provider mutation, preparation polling, publication, and Plex refresh. The
Watchlist worker never calls Prowlarr or TorBox directly.

This is preferred over extending a Watchlist lease across preparation because
an uncached transfer can take hours and must survive restarts. It is also
preferred over merging the two SQLite queues because Watchlist observation and
explicit acquisition have different identities, privacy boundaries, and
retention rules.

## Durable state and transitions

Add an optional `background_job_id` to each Watchlist record. A claimed movie
without a job ID is submitted once and then moved back to `acquiring` with a
short reconciliation delay. A claimed movie with a job ID reads the durable job:

- `queued`, `selected`, or `preparing`: retain the job ID and defer another
  reconciliation pass;
- `succeeded`: record the published object ID and finalize the Watchlist item;
- `failed` with `no_release` or `stalled`: clear the job ID and use the existing
  long `not_cached` cooldown so a later provider/indexer change may retry;
- other safe terminal failures: clear the job ID and use `manual_review`;
- `manual_review`: clear the job ID and finalize as `manual_review`.

A service or repository failure before a durable job is attached leaves the
Watchlist lease to expire. A failure after attachment retains the job ID. Every
Watchlist transition is lease-version checked, and process cancellation does
not cancel the bounded SQLite commit that records an already-created job ID.

## Configuration and safety

`BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED` remains the explicit opt-in. Its
meaning changes from cached-only acquisition to durable acquisition, including
provider download when a selected authorized release is not cached. The default
remains `false` in every Compose profile.

The Watchlist is always read-only. Credentials, Watchlist titles, external IDs,
release locators, TorBox object IDs, and acquisition job IDs are not returned by
the Watchlist status API. The browser continues to receive aggregate counts
only. Live acceptance uses legally redistributable open media.

## Testing and acceptance

- Domain tests validate optional durable-job identity and reject malformed IDs.
- Repository tests prove attach, defer, restart recovery, stale-lease rejection,
  and terminal cleanup of the linked job.
- Service tests prove submit/deduplicate, active reconciliation, success,
  no-release cooldown, manual review, cancellation-safe attachment, and serial
  processing.
- App integration tests prove enabled Watchlist acquisition uses the durable
  manager rather than the cached-only coordinator.
- Full Go race/coverage, frontend, lint, security, vulnerability, and Compose
  checks remain green.
- Brave acceptance proves one authorized open movie moves from Plex Watchlist to
  a published logical file and Direct Plays with forward and backward seeking
  while BlackPearl retains only rolling chunks.
