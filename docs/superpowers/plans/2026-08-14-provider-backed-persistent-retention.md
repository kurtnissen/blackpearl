# Provider-backed persistent retention implementation plan

**Goal:** Run the browser-selected TorBox/Plex library with non-evicting,
restart-durable range retention while preserving immediate seekable playback.

**Architecture:** Reuse the verified chunk engine behind an explicit retention
policy. Rolling remains quota-bound; persistent retains every fetched chunk in
a separate namespace. Browser setup selects one process-lifetime pool from the
configured storage mode.

## Constraints

- Keep the filesystem and `ReadAt` contracts unchanged.
- Never require a complete local media file before open or playback.
- Preserve legacy persistent fixture import.
- Preserve rolling quota behavior byte-for-byte.
- Keep credentials and provider locators out of local paths and diagnostics.
- Use test-first red-green-refactor for every production behavior.

### Task 1: Add retained range-cache policy

**Files:** `internal/cache/rolling.go`, `internal/cache/rolling_test.go`

- [x] Add failing tests for exact random reads, no eviction past a rolling-sized
  threshold, hit reuse, restart recovery without provider range refetches, and
  separate persistent/rolling namespaces.
- [x] Add a narrow persistent options/constructor surface backed by the shared
  chunk engine.
- [x] Make foreground/background reservations and recovery policy-aware while
  preserving rolling behavior.
- [x] Run `go test -race ./internal/cache` and commit
  `feat: retain provider ranges persistently`.

### Task 2: Permit browser setup in persistent mode

**Files:** `internal/config/config.go`, `internal/config/config_test.go`,
`cmd/blackpearl/app.go`, `cmd/blackpearl/app_test.go`

- [x] Add failing configuration tests for valid persistent browser setup and
  invalid quota/provider/legacy combinations.
- [x] Refine storage validation so only provider-backed browser persistent mode
  accepts range settings and prefetch controls.
- [x] Add failing app tests proving persistent setup selects one retained pool,
  restores a manifest, serves an exact read, and reuses it after runtime
  replacement.
- [x] Select the cache pool by storage mode without changing downstream setup,
  acquisition, Watchlist, NFS, or Plex-refresh services.
- [x] Run focused race tests and commit
  `feat: run browser setup with persistent retention`.

### Task 3: Compose and live acceptance

**Files:** `compose.torbox.yaml`, `scripts/test-torbox-compose.sh`, `README.md`,
`docs/architecture.md`, `docs/acceptance-evidence.md`

- [x] Add a storage-mode override while keeping rolling as the default and
  assert both rendered profiles remain isolated.
- [x] Run `make verify`, frontend lint/test/build, and all four Compose checks.
- [x] Rebuild the isolated profile in persistent mode; prove exact NFS ranges,
  restart reuse, Direct Play, and seeking in Brave.
- [x] Restore the user's normal rolling profile after acceptance unless they
  explicitly choose persistent as the operating default.
- [x] Record distinct automated, macOS live, Windows, and native-Linux evidence;
  commit `docs: verify persistent provider retention`.
