# Release hardening implementation plan

**Goal:** Remove known called vulnerabilities and make release checks cover the
entire BlackPearl repository and portable Docker surface.

### Task 1: Patch the Go build and dependency graph

**Files:** `go.mod`, `go.sum`, `Dockerfile`, `.github/workflows/ci.yaml`

- [x] Pin the current stable Go patch throughout local and container builds.
- [x] Upgrade vulnerable OpenTelemetry, gRPC, and Go networking modules.
- [x] Run `go mod tidy`, Go tests, and `govulncheck` until called findings are
  zero.
- [x] Commit `chore: update patched Go dependencies`.

### Task 2: Complete repository CI coverage

**Files:** `.github/workflows/ci.yaml`

- [x] Add a frozen-lockfile Bun job for lint, tests, and production build.
- [x] Run all credential-free Compose validation scripts.
- [x] Add an exact-version Go vulnerability job.
- [x] Preserve separate AMD64/ARM64 and kernel-FUSE checks.
- [x] Commit `ci: verify frontend security and compose profiles`.

### Task 3: Verify the release candidate locally

**Files:** `docs/acceptance-evidence.md`, `README.md`

- [x] Run full Go, frontend, dependency-audit, and Compose checks.
- [x] Build Linux AMD64 and ARM64 runtime images with Buildx.
- [x] Rebuild the normal rolling stack and verify readiness and Plex playback
  in Brave without leaving playback running.
- [x] Record exact local evidence and retain pending hosted CI, Windows, and
  native-Linux states.
- [ ] Commit `docs: record release hardening evidence`.
