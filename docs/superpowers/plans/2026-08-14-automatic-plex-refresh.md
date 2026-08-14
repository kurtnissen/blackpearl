# Automatic Plex Library Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically ask the isolated Plex server to rescan BlackPearl movie and TV libraries after a successful manifest publication.

**Architecture:** A strict Plex gateway discovers matching sections and refreshes them. A process-lifetime worker receives nonblocking post-publication notifications, coalesces them, and retries independently of the atomic setup transaction.

**Tech Stack:** Go 1.24, `net/http`, bounded JSON decoding, `context`, `go test -race`, Docker Compose, live Plex Web in Brave.

## Global Constraints

- Keep Plex and BlackPearl on disjoint Docker networks.
- Never persist, log, return, or place the Plex token in a URL.
- Refresh failure must never roll back successful publication.
- Match only `/blackpearl/Movies` and `/blackpearl/TV Shows` library roots.
- Use test-first red-green-refactor for every production behavior.

---

### Task 1: Strict multi-section Plex refresh gateway

**Files:**
- Create: `internal/plex/library.go`
- Create: `internal/plex/library_test.go`

**Interfaces:**
- Consumes: `TokenSource.Token(context.Context) (string, error)` and `*http.Client`.
- Produces: `plex.NewLibraryRefresher(baseURL string, tokens TokenSource, roots []string, client *http.Client) (*LibraryRefresher, error)`.
- Produces: `(*LibraryRefresher).Refresh(context.Context) error`.

- [x] Write a failing TLS-server test whose complete Plex JSON fixture contains movie, show, and unrelated sections; assert only the two exact BlackPearl section refresh paths are requested and every request receives the token header.
- [x] Run `go test ./internal/plex -run LibraryRefresher` and confirm the missing constructor fails compilation.
- [x] Implement bounded section discovery, exact root matching, stable unique section keys, header authentication, redirect rejection, and bounded body closure.
- [x] Add failing table tests for malformed/oversized responses, no matching sections, unauthorized responses, redirects, cancellation, invalid options, and secret-free errors; then implement each branch minimally.
- [x] Run `go test -race ./internal/plex` and commit `feat: refresh BlackPearl Plex libraries`.

### Task 2: Coalescing best-effort refresh worker

**Files:**
- Create: `internal/service/plexrefresh/worker.go`
- Create: `internal/service/plexrefresh/worker_test.go`

**Interfaces:**
- Consumes: `Refresher.Refresh(context.Context) error`.
- Produces: `plexrefresh.New(refresher Refresher, options Options) (*Worker, error)`.
- Produces: `(*Worker).Notify()` and `(*Worker).Run(context.Context)`.

- [x] Write a failing test proving many nonblocking notifications coalesce into one successful refresh after the debounce interval.
- [x] Run `go test ./internal/service/plexrefresh` and confirm the package/API is missing.
- [x] Implement a capacity-one signal channel, debounce timer, retry timer, injected error callback, and prompt context cancellation.
- [x] Add failing tests proving failure retries without another notification, success stops retrying, a notification during retry is coalesced, and cancellation stops an in-flight refresh; implement each branch minimally.
- [x] Run `go test -race ./internal/service/plexrefresh` and commit `feat: coordinate Plex refresh retries`.

### Task 3: Post-publication wiring and configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`
- Modify: `compose.torbox.yaml`
- Modify: `scripts/test-torbox-compose.sh`

**Interfaces:**
- Consumes: the Task 1 refresher, Task 2 worker, and the existing read-only Plex preferences token source.
- Produces: a successful `setupPublisher.Publish` notification only after NFS replacement and catalog activation.

- [x] Write failing config tests for enabled-without-URL, invalid URL, and disabled-with-URL combinations.
- [x] Add the two typed configuration fields and strict validation; run the focused config tests green.
- [x] Write failing publisher tests proving successful publication notifies once, failed NFS replacement never notifies, and notification has no error path capable of undoing publication.
- [x] Add a narrow notifier to `setupPublisher`, wire the refresher worker only when configured, and inject sanitized warning logging from `cmd`.
- [x] Update the TorBox profile with the host endpoint and Linux `host-gateway` mapping; extend the executable Compose assertion to preserve disjoint Plex/control networks.
- [x] Run `go test -race ./cmd/blackpearl ./internal/config` and all Compose safety scripts; commit `feat: trigger Plex scans after publication`.

### Task 4: Full and live acceptance

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Consumes: the complete running TorBox Compose profile.
- Produces: reproducible evidence distinguishing automated, Docker, and live Plex verification.

- [x] Run `make verify`, `cd web && bun run lint && bun run test && bun run build`, and all four Compose safety scripts.
- [x] Rebuild the TorBox stack, trigger one safe manifest re-publication, and verify the current Plex server log records a section refresh without printing its token.
- [x] In Brave, confirm the movie/TV libraries remain visible and the known H.264/AAC item still plays and seeks.
- [x] Record the exact evidence and remaining Windows/native-Linux status, run `git diff --check`, and commit `docs: record automatic Plex refresh evidence`.
