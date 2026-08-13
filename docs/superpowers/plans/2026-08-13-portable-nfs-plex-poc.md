# Portable NFS Plex POC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run BlackPearl and an unmodified Plex container on Docker Desktop for macOS so Plex scans, plays, and seeks the legal Milestone 1 video through a range-oriented NFS frontend.

**Architecture:** Add a read-only `internal/pearlnfs` adapter beside PearlFS. Both consume the catalog's existing `List` and `Open` methods, and NFS file reads call `domain.ReadHandle.ReadAt` with the protocol offset. A standalone portable Compose file publishes BlackPearl NFS only to the Docker daemon loopback and mounts it into Plex through Docker's built-in local NFS volume driver.

**Tech Stack:** Go 1.24, go-nfs NFSv3 server, go-billy filesystem adapter, SQLite, Docker Compose, official Plex container.

## Global Constraints

- Do not change `internal/pearlfs` behavior or remove the existing FUSE profile.
- Plex must remain the unmodified official image without added capabilities or devices.
- The NFS export and Plex library mount are read-only.
- All state uses project-scoped named volumes or repository-owned runtime paths.
- The transport must preserve arbitrary reads and logical sizes without requiring complete local bytes.
- Milestone 1 remains persistent-cache only; rolling cache and acquisition providers stay out of scope.

---

### Task 1: Filesystem mode configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.FilesystemMode string`, `Config.NFSAddr string`
- Valid modes: `fuse`, `nfs`

- [ ] **Step 1: Write failing parsing tests**

Add table-driven tests proving the default remains `fuse`, `nfs` is accepted with an absolute TCP listen address, and unknown modes or malformed NFS addresses are rejected.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/config -run 'TestParse.*Filesystem' -count=1`; expect compile failures because the fields do not exist.

- [ ] **Step 3: Implement typed configuration**

Add environment fields:

```go
FilesystemMode string `env:"BLACKPEARL_FILESYSTEM_MODE" envDefault:"fuse"`
NFSAddr        string `env:"BLACKPEARL_NFS_ADDR" envDefault:":2049"`
```

Validate `FilesystemMode` and require `net.SplitHostPort` success for NFS mode.

- [ ] **Step 4: Verify GREEN**

Run `go test ./internal/config -count=1` and `go vet ./internal/config`.

- [ ] **Step 5: Commit**

Commit as `feat: configure portable filesystem mode`.

### Task 2: Read-only PearlNFS adapter

**Files:**
- Create: `internal/pearlnfs/filesystem.go`
- Create: `internal/pearlnfs/filesystem_test.go`
- Create: `internal/pearlnfs/server.go`
- Create: `internal/pearlnfs/server_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes:

```go
type Catalog interface {
    List(context.Context) ([]domain.Media, error)
    Open(context.Context, string) (domain.ReadHandle, error)
}
```

- Produces:

```go
func New(ctx context.Context, catalog Catalog) (billy.Filesystem, error)
type Server struct { /* private fields */ }
func Start(ctx context.Context, address string, filesystem billy.Filesystem) (*Server, error)
func (s *Server) Close() error
func (s *Server) Wait() error
func (s *Server) Addr() net.Addr
```

- [ ] **Step 1: Write failing filesystem tests**

Use a real in-memory catalog fake with a generated `ReadHandle`. Assert the Plex movie hierarchy, read-only modes, logical one-terabyte size, arbitrary tail `ReadAt`, EOF handling, missing paths, and write rejection. The production break caught is any adapter that materializes a file or ignores the NFS offset.

- [ ] **Step 2: Verify filesystem RED**

Run `go test ./internal/pearlnfs -run TestFilesystem -count=1`; expect failure because the package implementation does not exist.

- [ ] **Step 3: Implement the minimal Billy filesystem**

Build an immutable directory snapshot from catalog paths. File `ReadAt` delegates directly to the opened domain handle and `Stat` returns `Media.Size`. All mutating Billy methods return `billy.ErrReadOnly`.

- [ ] **Step 4: Verify filesystem GREEN**

Run `go test ./internal/pearlnfs -run TestFilesystem -count=1`.

- [ ] **Step 5: Write failing server lifecycle tests**

Assert `Start` rejects a canceled context, listens on an ephemeral local port, and exits cleanly after `Close`. The production break caught is leaking the NFS listener or reporting readiness before binding.

- [ ] **Step 6: Verify server RED**

Run `go test ./internal/pearlnfs -run TestServer -count=1`; expect missing `Start` or lifecycle behavior.

- [ ] **Step 7: Implement the NFSv3 server lifecycle**

Wrap `go-nfs` with a listener-owned server and a bounded caching handler. Closing the listener stops serving, and `Wait` returns the terminal serve error only when it is not the expected closed-listener condition.

- [ ] **Step 8: Verify GREEN**

Run `go test -race ./internal/pearlnfs -count=1` and `go vet ./internal/pearlnfs`.

- [ ] **Step 9: Commit**

Commit as `feat: serve catalog through read-only NFS`.

### Task 3: Select FUSE or NFS at process wiring

**Files:**
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- Consumes: `config.Config.FilesystemMode`, `pearlnfs.New`, `pearlnfs.Start`
- Produces: one active filesystem server with `Close() error` and `Wait() error`

- [ ] **Step 1: Write failing run tests**

Add tests proving NFS mode starts the NFS dependency without invoking FUSE, FUSE mode preserves the current mount path, startup failures close SQLite, and cancellation closes/waits for the selected server.

- [ ] **Step 2: Verify RED**

Run `go test ./cmd/blackpearl -run 'TestRun.*NFS' -count=1`; expect the NFS dependency assertions to fail.

- [ ] **Step 3: Implement additive selection and lifecycle**

Construct the selected adapter after catalog import. Keep FUSE's current mount function unchanged. Generalize readiness wording from mounted to filesystem-ready and log either mount path or NFS address.

- [ ] **Step 4: Verify GREEN**

Run `go test -race ./cmd/blackpearl -count=1` and `go vet ./cmd/blackpearl`.

- [ ] **Step 5: Commit**

Commit as `feat: select FUSE or NFS frontend`.

### Task 4: Portable Compose and acceptance automation

**Files:**
- Create: `compose.portable.yaml`
- Create: `scripts/setup-portable-poc.sh`
- Create: `scripts/verify-portable-poc.sh`
- Create: `scripts/cleanup-portable-poc.sh`
- Create: `docs/macos-plex-poc.md`
- Modify: `Dockerfile`
- Modify: `README.md`

**Interfaces:**
- BlackPearl ports: diagnostics `127.0.0.1:8080`, NFS `127.0.0.1:20490`
- Plex URL: `http://localhost:32400/web`
- Plex library path: `/blackpearl/Movies`

- [ ] **Step 1: Add a failing Compose acceptance check**

The verification script must fail unless BlackPearl is ready, the official Plex container sees the exact fixture size through an NFS mount, Plex has indexed the title, and an arbitrary HTTP range from Plex matches the source bytes.

- [ ] **Step 2: Verify RED**

Run `./scripts/verify-portable-poc.sh`; expect failure because the portable stack is absent.

- [ ] **Step 3: Add portable runtime artifacts**

Build the POC image without requiring FUSE at runtime, publish NFS to daemon loopback, define the read-only Docker local NFS volume, and mount it into the official Plex image. Use project-scoped named volumes for database, cache, Plex config, and transcode.

- [ ] **Step 4: Add guarded setup and cleanup**

The setup script waits for both health endpoints, creates only the `BlackPearl POC` Movies library when missing, and triggers its scan. Cleanup names only the portable Compose project and its volumes.

- [ ] **Step 5: Verify GREEN on macOS Docker Desktop**

Run:

```sh
docker compose -f compose.portable.yaml up --build -d --wait
./scripts/setup-portable-poc.sh
./scripts/verify-portable-poc.sh
```

Expect readiness, NFS size/range checks, Plex index evidence, and exact Plex HTTP-range hash to pass.

- [ ] **Step 6: Document the manual play/seek check**

Document opening `http://localhost:32400/web`, signing in or claiming the isolated server if requested, playing `BlackPearl POC (2026)`, seeking near the end, and checking the Plex dashboard for Direct Play. State that client-observed Direct Play remains the manual acceptance gate.

- [ ] **Step 7: Commit**

Commit as `feat: add macOS portable Plex POC`.

### Task 5: Full verification and local launch

**Files:**
- Modify only if verification uncovers a tested defect.

**Interfaces:**
- Produces: a running local portable stack and a clean, reviewable branch.

- [ ] **Step 1: Run static and unit verification**

Run `gofmt -w` on changed Go files, `go vet ./...`, `go test -race -cover ./...`, and `./scripts/check-coverage.sh`.

- [ ] **Step 2: Run Compose acceptance again from clean volumes**

Run the guarded cleanup, launch, setup, and verification sequence. Preserve the final running stack for the user rather than cleaning it up.

- [ ] **Step 3: Review scope and safety**

Confirm `git diff main...HEAD --name-only` contains no changes under `internal/pearlfs`, and confirm Compose has no host media/library bind mount.

- [ ] **Step 4: Commit final documentation corrections if needed**

Use `docs: finalize portable Plex POC runbook` only when verification changes documentation.
