# Plex Watchlist Ingestion Implementation Plan

> Use red-green-refactor for every domain, gateway, repository, and service
> contract. Keep live polling disabled until observe-only evidence is recorded.

## Task 1: Model provider-neutral watchlist intent

- Add immutable watchlist item and source snapshot values.
- Validate GUID, media type, title byte length, and year.
- Map supported movies to existing acquisition search requests; retain shows as
  unsupported observations without inventing episode coordinates.
- Commit: `feat: model watchlist media intent`.

## Task 2: Read Plex account credentials safely

- Add a narrow token-source interface at the gateway consumer.
- Implement bounded Plex `Preferences.xml` and token-file readers with private,
  contextual, redacted errors.
- Re-read on demand so Plex reauthentication does not require a BlackPearl
  image rebuild.
- Commit: `feat: load Plex watchlist credentials`.

## Task 3: Build the bounded Plex watchlist gateway

- Add TLS contract tests before implementation.
- Fetch the current Discover-provider endpoint with header authentication,
  bounded pagination, redirect rejection, response limits, and cancellation.
- Skip malformed entries individually and never expose response bodies or
  titles in errors.
- Commit: `feat: read Plex watchlist movies`.

## Task 4: Persist the ingestion queue in SQLite

- Add schema and repository operations for snapshot upsert, lease claim,
  outcome completion, and aggregate status.
- Prove concurrent claims, expired-lease recovery, idempotency, cooldowns, and
  succeeded-item finality.
- Commit: `feat: persist watchlist ingestion queue`.

## Task 5: Coordinate observe-only ingestion

- Poll snapshots and upsert queue items without calling acquisition.
- Expose aggregate paired status only; do not return private titles from the
  public status endpoint.
- Wire disabled-by-default runtime configuration and cancellation.
- Commit: `feat: observe Plex watchlist safely`.

## Task 6: Enable serialized cached-only acquisition

- Claim one eligible movie, call the existing coordinator, and persist the
  normalized outcome.
- Distinguish no-cache cooldown, retryable provider failure, terminal ambiguous
  mutation, and success.
- Never acquire shows in this milestone.
- Commit: `feat: acquire cached Plex watchlist movies`.

## Task 7: Add portable Docker wiring

- Mount the isolated Plex config named volume read-only into BlackPearl.
- Keep Plex and BlackPearl on disjoint networks.
- Add config and Compose safety tests before enabling observe-only mode.
- Commit: `feat: wire portable Plex watchlist source`.

## Task 8: Verify and stage live acceptance

- Run full race, coverage, static, Compose, frontend, and credential scans.
- Record live observe-only item counts without titles.
- Keep acquisition disabled until Prowlarr's private authentication and
  authorized indexers are configured by the operator.
- Commit: `docs: record Plex watchlist ingestion evidence`.

