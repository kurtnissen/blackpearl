# Playback-aware read-ahead implementation plan

**Goal:** Cancel stale range-cache read-ahead when Plex seeks or closes a handle
without allowing background cancellation to fail foreground playback.

**Architecture:** One cancelable, generation-tagged read-ahead window per open
range handle. Background fetches inherit that window; demand fetches inherit the
process lifecycle and retry if a shared background operation was canceled.

## Constraints

- Preserve `domain.ReadHandle` and every filesystem adapter.
- Preserve global chunk coalescing, rolling quota, and persistent retention.
- Do not infer playback from metadata opens or add Plex API coupling.
- Use test-first red-green-refactor.

### Task 1: Handle-scoped cancellation and foreground retry

**Files:** `internal/cache/rolling.go`, `internal/cache/rolling_test.go`

- [ ] Add failing race-safe tests for seek cancellation, close cancellation,
  sequential-window continuity, and a second foreground reader retrying after
  shared background cancellation.
- [ ] Attach a parent context to each fetch call and route read-ahead through a
  handle-owned generation window.
- [ ] Cancel and replace the window on discontinuous reads; cancel it on close.
- [ ] Retry cache acquisition when a joined background call was canceled, while
  preserving normal foreground errors.
- [ ] Run the cache race suite repeatedly and commit
  `feat: cancel stale playback read-ahead`.

### Task 2: Regression and live acceptance

**Files:** `README.md`, `docs/architecture.md`, `docs/acceptance-evidence.md`

- [ ] Run `make verify`, frontend lint/test/build, and all Compose checks.
- [ ] Rebuild the normal rolling stack and verify health, manifest recovery,
  cache quota, Direct Play, and a far seek in Brave.
- [ ] Record automated versus live evidence plus remaining Windows/native-Linux
  status; commit `docs: verify playback-aware read-ahead`.
