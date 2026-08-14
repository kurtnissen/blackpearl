# Durable Release Fallback Design

## Outcome

BlackPearl automatic acquisition must not permanently fail because the first
ranked torrent is dead when another authorized release can satisfy the same
movie or episode. Candidate choice and cleanup must remain safe across process
restarts, and BlackPearl must never delete a provider object it did not create.

This slice keeps the existing one-service architecture, Watchlist authorization
policy, range-oriented filesystem, rolling cache, and Plex publication
transaction unchanged. It improves only the durable acquisition state machine.

## Root cause

The current resolver returns an ordered release list, but a background job
persists only the first release fingerprint. After TorBox classifies that object
as stalled, the job becomes terminal. A later Watchlist retry repeats the same
deterministic selection. The live legal Sintel run reproduced this path with an
Internet Archive result reporting zero seeders.

The current job also does not record whether `FindTorrentByHash` returned an
object already owned by the account or whether BlackPearl created the object.
That missing provenance makes automatic cleanup unsafe.

## Alternatives considered

### Cached-only preflight

Select only a TorBox-cached release. This avoids stalled downloads but removes
the uncached preparation behavior that the original product requires.

### In-memory fallback

Try the next search result when one stalls. This is small, but a restart loses
the attempted set and can repeat provider mutations. It contradicts the durable
job boundary.

### Durable bounded candidate plan

Persist a small ordered release plan, provenance for the attached account
object, and each candidate outcome. This costs a migration and additional state
transitions, but it preserves safety, restart recovery, and eventual provider
portability. This is the selected design.

## Candidate discovery and ordering

The provider factory supplies one searcher that combines the direct authorized
open-media gateway and configured Prowlarr results. One provider failure does
not discard valid results from the other. Search remains bounded by the job
operation timeout.

The worker validates, deduplicates, and retains at most five torrent candidates
with stable information hashes. Before persistence, the TorBox gateway checks
the bounded hash set for cached availability. Candidates are ordered as:

1. cached candidates in resolver rank order;
2. uncached candidates in resolver rank order.

Only normalized release metadata is persisted. Download URLs, magnet links,
torrent bytes, credentials, signed URLs, and provider response bodies remain
ephemeral.

## Durable data model

`acquisition_job_candidates` stores:

- job ID and zero-based ordinal;
- normalized provider, title, size, indexer, information hash, and optional
  seed count;
- outcome: `pending`, `selected`, `stalled`, `missing`, or `unplayable`.

`acquisition_jobs` adds:

- `selected_candidate_ordinal`, default `-1`;
- `created_by_job`, default false.

Planning is one SQLite transaction: insert the bounded candidates, mark ordinal
zero selected, and transition the leased job from queued to selected. Candidate
rows cascade with their job. Existing jobs created before the migration have no
candidate plan and retain the legacy single-selection terminal behavior; the
migration does not infer ownership or delete anything.

## Worker transitions

### Prepare

For the selected hash, the worker first reconciles the TorBox account:

- a unique existing object is attached with `created_by_job=false`;
- a missing object is materialized and created, then attached with
  `created_by_job=true`;
- an ambiguous account match enters manual review without mutation.

### Inspect and publish

- not ready: defer the same candidate;
- playable: atomically publish through the existing setup transaction, then
  succeed the job;
- stalled, disappeared, or no playable media: abandon the selected candidate
  and advance.

### Safe abandonment

If `created_by_job=false`, BlackPearl never deletes the account object. It marks
the candidate outcome and advances.

If `created_by_job=true`, BlackPearl requests deletion of that exact provider
object before advancing. A definite deleted or already-missing result permits
the next candidate. An unauthorized, unavailable, malformed, or ambiguous
cleanup result enters manual review; BlackPearl does not retry the destructive
call automatically.

Advancement atomically records the current outcome and either clears the
attached object, selects the next pending candidate, and returns the job to
selected, or moves the job directly to failed with the most specific public
terminal code when no candidates remain. The Watchlist worker keeps
the item in `acquiring` while alternatives remain and applies its normal
cooldown only after the background job is terminal.

## Provider boundary

The durable worker consumes narrow capabilities:

- `CachedTorrents` for read-only cache prioritization;
- `FindTorrentByHash` for reconciliation;
- `CreateTorrent` for one explicit mutation;
- `InspectCreatedTorrent` for readiness and candidate discovery;
- `DeleteCreatedTorrent` for exact owned-object cleanup.

TorBox implements deletion with its authenticated control endpoint. The gateway
validates provider and positive object ID before sending the request, does not
follow redirects, bounds the response, sanitizes errors, and never retries.

## Limits and safety

- At most five candidates and therefore at most five new provider objects per
  job.
- Jobs remain serialized; no parallel provider mutations.
- Automatic Watchlist intake remains opt-in and post-baseline only.
- Existing provider objects are never deleted automatically.
- Published objects are never deleted by fallback cleanup.
- Cleanup ambiguity always stops in manual review.
- All credentials and locators remain out of durable job rows, logs, and HTTP
  responses.

## Acceptance

- Repository race tests prove candidate-plan atomicity, restart recovery,
  stale-lease rejection, and one-winner advancement.
- Worker tests prove cached-first planning, created-versus-existing provenance,
  safe cleanup, restart continuation, candidate exhaustion, and successful
  publication after at least one failed candidate.
- Gateway contract tests prove exact deletion, no retry, strict redirect and
  response bounds, authentication, cancellation, not-found handling, and secret
  redaction.
- Existing single-candidate jobs preserve their current behavior after the
  migration.
- Full Go, frontend, vulnerability, and Compose gates remain green.
- Live acceptance uses only a legally usable open movie, verifies no failed
  BlackPearl-owned object remains, and distinguishes provider exhaustion from a
  successful Plex publication.

## Deferred work

Usenet candidates, adaptive codec scoring, cross-provider cost policy, manual
candidate controls, and TV-show episode intent are separate slices. This design
keeps the candidate plan provider-neutral so those additions do not require a
new job state machine.
