# Durable background acquisition design

## Outcome

An already paired user can explicitly ask BlackPearl to prepare an authorized
movie or episode when no instant TorBox-cached release exists. BlackPearl owns a
durable job, lets TorBox prepare the selected torrent, survives process and
container restarts, publishes the completed media through the existing atomic
manifest transaction, and triggers the existing Plex refresh notifier.

The existing cached-only `/api/acquisition/acquire` path remains unchanged. The
new path is opt-in and does not turn Plex Watchlist observation into automatic
uncached downloading.

## Safety and persistence contract

BlackPearl persists only validated search intent, a stable torrent info hash,
safe release metadata, a TorBox object ID after it is known, state, progress,
timestamps, and a public-safe error code. It never persists TorBox or Prowlarr
credentials, signed CDN links, Prowlarr proxy URLs, magnet URLs, raw torrent
bytes, or provider error text.

Before the first TorBox mutation, the worker durably records the selected info
hash. On every subsequent attempt it reconciles the TorBox account by that hash
before creating anything. This makes the provider mutation restart-safe: a
crash after TorBox accepts the torrent but before BlackPearl records the returned
object ID does not cause a blind duplicate create.

The worker is serialized and lease-based. A process can reclaim expired work,
but a stale lease cannot commit state. Definitely terminal content failures
become `failed`; an indeterminate provider mutation that cannot be reconciled
becomes `manual_review` and is never retried automatically.

## Domain state machine

```text
queued
  -> resolving
  -> selected
  -> preparing
  -> publishing
  -> succeeded

Any leased state -> retryable -> resolving/selected/preparing
Any state after an ambiguous mutation -> manual_review
Invalid/no matching release/no playable file -> failed
```

`resolving` and `publishing` are lease-owned transient states. Expired leases
are reclaimable. `selected` includes the stable info hash and can safely resume.
`preparing` includes the provider object ID and is polled without repeating the
create mutation. Progress is advisory, clamped to 0-100, and never controls the
correctness transition to ready media.

## Provider-neutral boundaries

The acquisition service consumes narrow interfaces defined at the service:

- a search provider returns ranked validated releases;
- a release materializer turns one ephemeral release into a bounded transient
  torrent input;
- a preparation gateway reconciles by stable fingerprint, creates with an
  explicit cached-only or allow-download policy, and inspects an object;
- the existing publisher atomically exposes selected media.

The first implementation is Prowlarr plus TorBox, but the job and repository do
not name either product. A future Usenet implementation can add another
materialized input and preparation gateway without changing PearlFS,
PearlCache, or the range-oriented playback contracts.

Prowlarr materialization accepts a validated magnet or downloads a bounded
`.torrent` only from the configured Prowlarr origin. Before TorBox receives a
torrent file, BlackPearl extracts the raw bencoded `info` value and verifies its
SHA-1 info hash against the selected release. Redirects, oversized responses,
non-torrent bodies, and hash mismatches are rejected before mutation.

## API and UI

The paired setup API adds:

- `POST /api/acquisition/jobs` to submit explicit preparation intent;
- `GET /api/acquisition/jobs` to list recent privacy-scoped jobs for the paired
  local browser; and
- `GET /api/acquisition/jobs/{id}` to poll one job.

Submission returns `202 Accepted` with the durable job. Repeating the same
active intent returns the existing active job. The UI first retains the instant
button. When no cached result exists, it offers a clear second action:
`Prepare through TorBox`. The progress card explains that this can take time,
continues after closing the page, and will appear in Plex automatically.

Job responses expose title and release status only to an already paired browser
through the existing session, CSRF, bootstrap, host, and origin controls.

## Runtime lifecycle

The browser-setup process always opens the acquisition job database and starts
one worker. The worker uses the process lifecycle context, not an HTTP request
context. Provider calls have bounded per-attempt timeouts. Shutdown stops new
claims and lets the active call end through context cancellation; the lease
later makes unfinished work recoverable.

The existing setup publisher and Plex refresh notifier remain the only
publication path. No production media or Plex library path is modified.

## Acceptance

- Existing cached-only acquisition tests and the verified Big Buck Bunny Direct
  Play path remain green.
- A unit/integration test proves a job persists a hash before provider create.
- A simulated crash after create is reconciled by hash without a second create.
- A restart resumes a preparing object and eventually publishes it.
- Stale leases cannot commit, and ambiguous mutations do not auto-retry.
- Prowlarr torrent materialization is bounded, same-origin, and info-hash
  verified.
- API schema validation, pairing authorization, UI progress, and error states
  have tests.
- A legally usable uncached test release completes live, appears in Plex,
  Direct Plays, and seeks while the complete logical media file never exists in
  BlackPearl's cache.
- Full race, coverage, lint, vulnerability, frontend, Compose, and architecture
  gates pass.

