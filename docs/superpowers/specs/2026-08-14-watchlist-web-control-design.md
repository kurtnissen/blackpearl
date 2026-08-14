# Watchlist Web Control

## Goal

Let a paired local user turn automatic Plex Watchlist movie acquisition on or
off in the BlackPearl WebUI without editing environment variables or rebuilding
containers.

## Safety model

Automatic acquisition remains off for a fresh installation. The existing
`BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED` value seeds the durable setting only
when no setting has ever been stored, preserving current deployments. After
that first seed, the SQLite value is authoritative across restarts.

The existing pairing, loopback Host, Origin, CSRF, bootstrap, and session
checks protect the mutation. Plex cannot reach the control network. The API
returns only the boolean policy and aggregate queue state; it returns no title,
identifier, provider locator, or credential.

## Architecture

The Watchlist repository owns one singleton policy row. It exposes narrow
methods to initialize, read, and update the boolean. `Claim` includes the
setting in its atomic eligibility query, so disabling the policy immediately
prevents new provider work and retries without a race between service checks
and the lease transaction.

The observer reads the setting on every sync. Its existing first-sync baseline
remains unchanged: enabling automatic acquisition never makes previously
observed rows eligible. Only movies inserted during later enabled snapshots are
eligible. Shows remain observation-only.

The Watchlist worker runs whenever Watchlist observation is enabled. When the
policy is off, its atomic claim returns not found and it idles. An acquisition
job that was already submitted remains durable and may finish; disabling does
not attempt an ambiguous provider cancellation or remove published media.

The service exposes `SetAcquisitionEnabled(context.Context, bool)` through the
same paired handler dependency used for status. OpenAPI adds
`PUT /api/watchlist/settings` with an exact boolean body. The React panel shows
one ordinary checkbox/switch action with explicit copy: existing Watchlist
items stay untouched, and only newly added movies become automatic.

## Error handling

- Repository failures map to the existing public-safe unavailable error.
- A failed write leaves the previous policy active and the UI reports failure.
- Unpaired, foreign-origin, missing-CSRF, and malformed requests fail before
  service mutation.
- Disabling never deletes a provider object, queue row, cached chunk, manifest
  item, or Plex library item.

## Verification

Tests are written first for migration/restart persistence, initialize-once
semantics, atomic claim gating, observer dynamic policy, handler authorization
and schema bounds, typed frontend API validation, and user-visible toggle
states. The complete race, coverage, lint, vulnerability, frontend build,
dependency audit, and Compose safety gates must remain green. Live macOS
acceptance toggles off and on through Brave, confirms status after a BlackPearl
restart, and leaves the user's current Watchlist and eight-item manifest
unchanged.

## Acceptance criteria

1. A paired browser can enable or disable automatic Watchlist movie intake.
2. The selection survives BlackPearl restart.
3. Disabled policy atomically prevents new claims and retries.
4. Enabling cannot drain items observed before the current process baseline.
5. Existing active jobs and published media are never deleted or canceled.
6. Fresh installs default off; existing environment-enabled deployments seed on
   exactly once.
