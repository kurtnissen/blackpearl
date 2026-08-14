# Cached TorBox Acquisition Implementation Plan

> Follow red-green-refactor for each production change. Provider tests use
> local TLS servers only; do not mutate a real account during this plan.

## Task 1: Model cached acquisition results

- Add provider-neutral acquired-object and acquired-media values under
  `internal/acquisition`.
- Validate provider identity, positive remote object IDs, search intent, and
  candidate immutability.
- Write failing table-driven tests first, then the minimum implementation.
- Commit: `feat: model cached acquisition results`.

## Task 2: Implement TorBox cache lookup

- Add TLS gateway contract tests for `POST /torrents/checkcached`.
- Require bearer auth, JSON hash batches, object format, bounded bodies,
  cancellation, auth preservation, and secret redaction.
- Map only returned normalized hashes as cached.
- Commit: `feat: check TorBox torrent cache`.

## Task 3: Implement cached-only TorBox creation

- Add TLS gateway contract tests for multipart creation.
- Assert `add_only_if_cached=true`, `allow_zip=false`, a non-seeding preference,
  and a validated/synthesized magnet.
- Parse the positive created torrent ID and returned hash.
- Do not retry transport or ambiguous server failures.
- Commit: `feat: create cached TorBox torrents`.

## Task 4: Inspect one created TorBox torrent

- Refactor discovery mapping without changing existing account discovery.
- Add an ID-scoped, fresh inspection operation that returns eligible media
  candidates only when the torrent is complete and present.
- Commit: `feat: inspect acquired TorBox media`.

## Task 5: Orchestrate ranked cached acquisition

- Add `internal/service/acquisition` with consumer-owned search, cache,
  creation, inspection, and publication interfaces.
- Test no-cache behavior, ranked fallback, cancellation, bounded readiness
  polling, movie/episode selection, and sanitized failures.
- Commit: `feat: orchestrate cached acquisition`.

## Task 6: Reuse atomic manifest publication

- Write setup-service tests for append, same-path replace, capacity, and
  rollback on prepare/save/publish failure.
- Extract the current transaction internals without changing manual setup.
- Implement the narrow publisher boundary consumed by acquisition.
- Commit: `feat: publish acquired media atomically`.

## Task 7: Verify and document

- Run `make verify`.
- Run every Compose safety check.
- Run web lint, Vitest, and production build.
- Run `git diff --check` and the local credential scan without recording the
  credential in source or command documentation.
- Update architecture and acceptance evidence with mocked-vs-live truth.
- Commit: `docs: record cached acquisition foundation`.
