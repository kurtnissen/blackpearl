# Cached TorBox Acquisition Design

## Goal

Turn a ranked, explicitly authorized torrent search result into a seekable
TorBox-backed BlackPearl media candidate without ever starting an uncached
download. The result must remain provider-neutral above the gateway and must be
publishable through the existing atomic setup/runtime transition.

## Scope

This slice implements the backend contract and mocked provider evidence. It
does not add public indexers, configure Prowlarr automatically, mutate the live
TorBox account during tests, or claim end-to-end automatic Plex acquisition.
The existing manual manifest flow remains available throughout.

## Safety invariants

1. Only validated torrent releases with a stable info hash are eligible.
2. BlackPearl checks TorBox cache availability before creation.
3. Torrent creation always sends `add_only_if_cached=true`; this is the
   authoritative race-safe guard if cache state changes after the preflight.
4. Creation disables archive wrapping so Plex receives individual files.
5. Provider tokens, magnets, download URLs, and provider response bodies never
   appear in public errors or logs.
6. Cancellation and bounded response bodies apply to every request.
7. A provider mutation is never retried automatically after an ambiguous
   transport failure.

## Boundaries

### TorBox gateway

The gateway maps three external operations and contains no selection policy:

- batch cache lookup for normalized torrent hashes;
- cached-only torrent creation from a validated magnet or synthesized BTIH
  magnet;
- inspection of one account torrent by ID, mapped to eligible media
  candidates.

### Acquisition service

The service receives already-ranked releases, asks the gateway which hashes
are cached, and tries cached releases in rank order. After creation, it polls
inspection with a short bounded policy because a cached account object may not
be immediately visible. It selects one video deterministically:

- an episode requires an exact `SxxExx` token and prefers the largest matching
  video;
- a movie prefers a complete normalized title match, then the largest video;
- obvious samples and non-video files are already excluded by the gateway.

The result is a provider-neutral media candidate plus the original search
intent. It is not a persisted release locator.

### Atomic publication

The acquisition service calls a narrow catalog publisher. The setup service
implements that boundary by appending or replacing one validated item, then
using the same prepare, readiness probe, durable token/manifest save, atomic
runtime publish, and rollback path as manual setup. Provider creation cannot be
rolled back safely, but a failed publication leaves the prior Plex namespace
and persisted manifest active.

## Failure behavior

- No cached ranked result: stable `not cached` domain error; no mutation.
- Authentication failure: preserved as the existing public unauthorized error.
- Ambiguous create failure: sanitized unavailable error; no automatic retry.
- Created object not ready before the bound: sanitized unavailable error; no
  catalog change.
- No eligible video inside the created object: invalid-media error; no catalog
  change.
- Persistence or NFS publication failure: existing manifest/runtime rollback.

## Acceptance

- Race tests prove cached-only request construction, response bounds,
  cancellation, redaction, and no retry after ambiguous create failure.
- Service tests prove rank order, no-mutation cache misses, deterministic
  episode/movie selection, bounded polling, and publication only after a ready
  account object.
- Setup tests prove acquired-item append/replace remains atomic and preserves
  the prior manifest on prepare, save, or publish failure.
- Full backend, web, and Compose safety verification remains green.
