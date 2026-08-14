# Provider preparation progress

## Goal

Show durable, meaningful preparation progress while an authorized acquisition
provider is still obtaining a selected release. A provider may take minutes or
hours to become range-readable; BlackPearl must not look stuck at zero while
that work is progressing.

## Scope

This slice extends the existing provider-neutral inspection boundary. It does
not change candidate search, provider mutation, publication, cache behavior,
Plex paths, or playback. TorBox is the first adapter to populate progress.

## Design

`Preparer.InspectCreatedTorrent` returns an acquisition-domain inspection
value containing:

- eligible media candidates when the provider object is ready; and
- an integer progress percentage in the inclusive range 0 through 100.

The domain constructor rejects out-of-range values and prevents provider
specific fields from leaking into the service. TorBox maps its documented
floating-point `progress` value to a bounded integer. A ready object reports
100. A not-ready or stalled object may return its last valid progress together
with the existing sentinel error.

The durable worker persists progress only for `ErrNotReady`. For one selected
candidate, persisted progress is monotonic: a stale or temporarily lower
provider value cannot move the UI backward. Candidate fallback continues to
reset progress to zero through the existing `Advance` transition. Successful
publication continues to commit 100 through `Succeed`.

The cached-only interactive acquisition service consumes candidates from the
same inspection result and otherwise keeps its current retry policy. It does
not expose intermediate progress because that synchronous flow is deliberately
short and bounded.

## Error handling

- Missing progress decodes as zero and preserves the durable job's prior value.
- Non-finite or out-of-range provider progress is treated as malformed external
  data and fails the inspection safely.
- `ErrNotReady`, `ErrStalled`, authorization, not-found, and ambiguous-object
  behavior remain unchanged.
- No credential, object URL, or provider response body is persisted or returned
  to the browser.

## Verification

Tests are written before implementation and cover:

- valid TorBox progress normalization and ready-at-100 behavior;
- rejection of malformed progress;
- monotonic durable progress across repeated worker polls;
- restart-readable progress through the SQLite repository and existing API;
- unchanged stalled, fallback, and successful publication behavior; and
- the complete race, coverage, lint, vulnerability, frontend, build, and
  Compose safety gates.

## Acceptance criteria

1. A preparing durable job reports provider progress from 0 through 99.
2. Progress never decreases for the same selected candidate.
3. Progress survives process restart because it is committed to SQLite.
4. Candidate fallback resets progress to zero.
5. Successful publication reports 100.
6. The provider interface remains generic and contains no TorBox-specific type.
