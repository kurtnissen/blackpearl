# Plex Watchlist Ingestion Design

## Goal

Turn a normal Plex Watchlist action into a durable BlackPearl movie request.
The first increment is movie-only: a watchlisted movie becomes eligible for the
existing Prowlarr search and cached-only TorBox transaction. TV shows remain
visible but are not acquired until BlackPearl has an explicit season/episode
policy.

## Safety and authority

- Watchlist access is read-only. BlackPearl never adds, removes, or edits Plex
  Watchlist items.
- The Plex account token is accepted through an injected token source and is
  sent only in the `X-Plex-Token` header. It is never logged, returned by an
  API, placed in a query string, or persisted in the ingestion queue.
- The bundled Docker profile may read the isolated Plex `Preferences.xml`
  through a read-only named-volume mount. Existing deployments can instead
  provide a dedicated token file. BlackPearl never scans arbitrary host paths.
- A watchlist item contains metadata intent, not a release locator. Prowlarr
  and TorBox selection remain behind the existing acquisition service.
- Polling must never cause repeated ambiguous provider mutations. Durable queue
  state is committed before acquisition and records success, no-cache, and
  retryable failure outcomes.

## Source contract

The initial adapter calls Plex's current Discover provider watchlist endpoint
with bounded pagination and JSON responses. The route was observed from Plex
Web 4.156.0 and live-probed successfully on 2026-08-14, but it is not part of
Plex's documented stable PMS API. The adapter therefore remains isolated and
replaceable. Plex's officially supported Watchlist RSS feed is the fallback if
the Discover provider contract changes.

Each accepted item requires:

- a canonical Plex GUID;
- type `movie` or `show`;
- a non-empty title of at most 200 UTF-8 bytes; and
- a release year from 1888 through 2100.

Malformed items are skipped individually. Responses, pagination, redirects,
and time are bounded. Provider errors are normalized and never include bodies,
tokens, URLs, or watchlist titles.

## Durable queue

SQLite stores provider-neutral records keyed by `(source, external_id)`:

- normalized movie intent;
- first/last observed timestamps;
- state: `pending`, `acquiring`, `succeeded`, `not_cached`, `retryable`, or
  `manual_review`;
- attempt count and next-attempt timestamp; and
- the published BlackPearl object ID only after success.

An upsert never reopens a succeeded item. A crashed `acquiring` record becomes
retryable only after a lease expires. `not_cached` uses a long cooldown because
the prior attempt made no TorBox mutation. Known transient failures use a
bounded retry cooldown. Ambiguous create failures retain a terminal/manual-review
state rather than being retried automatically.

## Polling flow

1. Fetch and validate a bounded Watchlist snapshot.
2. Upsert supported movie intents into SQLite.
3. Claim at most one eligible queue record under a lease.
4. Invoke the existing acquisition coordinator.
5. Persist the public outcome before claiming another item.
6. Expose only aggregate queue state to the paired UI.

The poller runs on a process-lifetime context, serializes acquisitions, and
uses bounded configured intervals. Startup restore and manual browser acquisition keep
their existing locks and transactions.

## Acceptance

- TLS contract tests prove endpoint, header authentication, pagination,
  bounds, redirect rejection, cancellation, malformed-item isolation, and
  secret-safe errors.
- SQLite tests prove idempotent upsert, leasing, crash recovery, cooldowns,
  success finality, and concurrent claim serialization under `-race`.
- Service tests prove one acquisition at a time, movie-only behavior, no-cache
  retry policy, and no repeated ambiguous mutation.
- Compose tests prove the Plex config mount is named-volume-only and read-only,
  with Plex still network-isolated from BlackPearl.
- Live acceptance first runs in observe-only mode, then acquires one authorized
  cached watchlist movie only after private Prowlarr indexers are configured.
