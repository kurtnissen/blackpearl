# Release hardening design

## Outcome

BlackPearl builds with a supported, patched Go toolchain and dependency graph,
and its repository CI checks the Go service, setup UI, Compose profiles,
vulnerability status, and Linux image portability before a release claim.

This slice does not publish a repository or claim Windows/native-Linux runtime
acceptance. It makes those later runs trustworthy and keeps the working macOS
rolling stack unchanged until the replacement image is verified.

## Toolchain and dependencies

Pin the Go language and container builder to the current stable patch release
rather than an unsupported major line. Upgrade only modules reported on called
paths by `govulncheck`, allowing Go module resolution to align their transitive
OpenTelemetry, gRPC, and `x/*` dependencies. The resulting graph must compile,
pass the race detector, and report no known called vulnerabilities.

The exact Go patch appears in `go.mod`, the Docker builder, and the privileged
FUSE smoke image. GitHub Actions reads `go.mod` for ordinary jobs, preventing a
different implicit toolchain from silently testing the repository.

## CI contract

CI has independent jobs for:

- Go vet, race tests, coverage, and lint;
- the pinned Go vulnerability scan;
- Bun install, TypeScript/ESLint, Vitest, and the production UI build;
- every Compose safety profile without provider credentials;
- Linux AMD64 and ARM64 image builds; and
- the privileged kernel-FUSE smoke test.

Jobs use lockfiles and pinned tool versions. Provider-backed live acceptance,
Plex account actions, and secrets remain outside hosted CI.

## Acceptance

- `govulncheck ./...` reports no vulnerabilities in called code.
- Full Go race, coverage, vet, and lint checks pass.
- Frontend lint, Vitest, build, and package audit pass.
- All four Compose validation scripts pass.
- local Buildx produces both Linux AMD64 and ARM64 runtime images.
- the normal macOS rolling stack returns ready and Plex playback still works in
  Brave after rebuilding.
- documentation separates local evidence from future hosted CI, Windows, and
  native-Linux evidence.
