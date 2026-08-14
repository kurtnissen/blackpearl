# Paired Acquisition Console Implementation Plan

> Use red-green-refactor for backend/domain/API behavior. Read the frontend
> design and anti-generic-UI skill instructions before editing React or CSS.

## Task 1: Model and persist search-provider settings

- Add immutable generic search-provider settings with strict endpoint and
  credential validation.
- Build a private atomic acquisition-settings repository with bounded reads.
- Add race tests for save/reopen, file modes, cancellation, corrupt/oversized
  state, and secret-free errors.
- Commit: `feat: persist acquisition provider settings`.

## Task 2: Probe Prowlarr safely

- Add a read-only readiness operation against `/api/v1/health`.
- Test path-prefix URLs, API key header, redirects, response bounds,
  cancellation, authentication mapping, close failures, and redaction.
- Commit: `feat: probe Prowlarr configuration`.

## Task 3: Build the acquisition coordinator

- Add consumer-owned repository, setup credential, provider factory, and
  publisher interfaces.
- Configure validates and probes before saving.
- Acquire loads saved credentials, composes resolver plus cached TorBox
  acquisition, and returns only provider-neutral acquired media.
- Test all success, missing-config, authentication, cancellation, and sanitized
  failure paths.
- Commit: `feat: coordinate configured acquisition`.

## Task 4: Specify and implement paired HTTP routes

- Extend `api/openapi.yaml` first.
- Write handler tests for status, settings, acquire, shared pairing checks,
  bounded strict JSON, public error mapping, and non-echoed secrets.
- Add optional acquisition service wiring without changing existing setup
  behavior.
- Commit: `feat: expose paired acquisition API`.

## Task 5: Wire the runtime

- Construct the private settings repository and coordinator in browser setup.
- Use isolated bounded clients and existing TorBox/setup factories.
- Add app-level mocked Prowlarr/TorBox acquisition acceptance without live
  provider mutation.
- Commit: `feat: wire browser acquisition runtime`.

## Task 6: Add the non-technical acquisition UI

- Add typed API functions and runtime validators first.
- Add ready-screen Prowlarr connection and movie/episode acquisition flow.
- Preserve the existing visual system, responsive behavior, reveal controls,
  and manual manifest fallback.
- Add Vitest behavior coverage, then browser visual/interaction checks.
- Commit: `feat: add acquisition console`.

## Task 7: Add optional Prowlarr Compose support

- Add an isolated, loopback-only optional Prowlarr service and private config
  volume without sharing BlackPearl's control network with Plex.
- Test rendered Compose safety and document the one-time Prowlarr indexer setup.
- Commit: `feat: add optional Prowlarr profile`.

## Task 8: Verify and record evidence

- Run full backend race/coverage/static checks.
- Run every Compose safety check.
- Run web lint, Vitest, and production build.
- Run whitespace and local credential scans.
- Update README, architecture, and acceptance evidence without claiming live
  provider or Plex evidence that was not observed.
- Commit: `docs: record paired acquisition console`.
