# Milestone 1 Acceptance Evidence

Evidence states are deliberately separate. Unit tests, a container build, a kernel FUSE test, Plex scan/range evidence, and client playback prove different things.

## Current verified evidence — 2026-08-13

Tested from the `research/portable-filesystem` branch on an Apple Silicon Mac with Docker Desktop's Linux ARM64 VM.

| Gate | Result | Evidence |
|---|---|---|
| Go race tests | Pass | `go test -race ./...` |
| Go coverage | Pass | 83.2% statements from `go test -race -coverprofile=coverage.out ./...` |
| Static analysis | Pass | `go vet ./...` |
| Compose isolation | Pass | Rendered configuration checked by `scripts/test-compose-paths.sh`; every bind is under `runtime/`, Plex media is read-only `rslave`, and only BlackPearl receives FUSE privileges |
| POC image build | Pass | `blackpearl:poc` built for local Linux ARM64 |
| Fixture media profile | Pass | 1280x720 H.264 `yuv420p` video, AAC 48 kHz mono audio, MP4 fast-start fixture |
| Packaged FUSE bytes | Pass | The exact POC image mounted FUSE in a privileged Linux container; fixture and virtual SHA-256 matched and non-sequential range hashes matched |
| Large logical object regression | Pass | PearlFS test read the final four bytes of a generated 1 TiB logical object without storing the object |
| Portable Compose isolation | Pass | No bind mounts, capabilities, or devices; all published ports use loopback and the Plex NFS volume is read-only |
| PearlNFS range seam | Pass | PearlNFS tests read the final four bytes of a generated 1 TiB logical object through `ReadAt` |
| macOS Plex NFS mount | Pass | Unmodified official Plex container reads the exact 3,417,699-byte virtual MP4 through Docker's local NFS volume |
| Plex library scan | Pass on macOS | Plex indexed `BlackPearl POC (2026)` as 1280x720 H.264/AAC MP4 from `/blackpearl/Movies` |
| Plex original-media range | Pass on macOS | Plex returned HTTP 206 for bytes 1,048,576-1,114,111 and the 64 KiB SHA-256 matched the source |
| Plex client Direct Play and seek | Pass on macOS | Plex Web played the fixture through the portable stack; the server decision log recorded `MDE=1000, Direct play OK`, served the original MP4, and timeline events confirmed seeks from 6 seconds to 3 seconds and back to 6 seconds |
| Cross-container Plex mount | Pending Ubuntu | Docker Desktop bind propagation cannot prove this Linux-host behavior |

The current result includes locally verified FUSE and portable NFS adapters plus macOS Plex scan, exact-range, client Direct Play, and seek evidence. Windows and native-Linux portability remain unverified.

## Acceptance checklist

- [x] Range-oriented domain, core, cache, acquisition, and FUSE contracts do not expose local paths.
- [x] Persistent POC imports a legally usable generated fixture.
- [x] Rolling mode and quota are represented from the start and rolling startup fails explicitly.
- [x] Race tests pass.
- [x] Coverage is at least 80%.
- [x] `go vet` passes.
- [x] Compose safety checks pass.
- [x] Kernel-mounted FUSE reads exact complete bytes and arbitrary offsets.
- [x] Containerized fixture has the intended H.264/AAC profile.
- [x] Portable Docker profile mounts PearlNFS into an unmodified Plex image.
- [x] Plex scans the fixture through PearlNFS and serves arbitrary source ranges unchanged.
- [x] A macOS Plex Web client Direct Plays the fixture and manual seeks succeed.
- [ ] CI passes after publishing the repository.
- [ ] Both Linux AMD64 and ARM64 image builds pass in CI.
- [ ] Ubuntu host propagation makes the file readable in the Plex container.
- [ ] Plex scans `BlackPearl POC (2026)`.
- [ ] Plex begins Direct Play and seeking succeeds.
- [ ] Stack shutdown unmounts PearlFS cleanly on the Ubuntu host.

## Ubuntu/Plex evidence record

Fill this section during the acceptance run.

```text
Date:
Operator:
Git commit:
Ubuntu release:
Architecture:
Docker version:
Compose version:
Plex image: plexinc/pms-docker:1.43.0.10492-121068a07

scripts/test-compose-paths.sh: PASS / FAIL
scripts/prepare-ubuntu-poc.sh: PASS / FAIL
scripts/verify-fuse.sh: PASS / FAIL
Virtual file visible inside Plex container: PASS / FAIL
Plex scan title/result:
Plex Dashboard video decision:
Plex Dashboard audio decision:
Seek result:
Clean shutdown/unmount: PASS / FAIL

Notes or captured log/screenshot locations:
```
