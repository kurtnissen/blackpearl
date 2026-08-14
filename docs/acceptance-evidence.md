# Milestone 1 Acceptance Evidence

Evidence states are deliberately separate. Unit tests, a container build, a kernel FUSE test, Plex scan/range evidence, and client playback prove different things.

## Current verified evidence — 2026-08-14

Tested from `main` on an Apple Silicon Mac with Docker Desktop's Linux ARM64 VM.

| Gate | Result | Evidence |
|---|---|---|
| Go race tests | Pass | `go test -race ./...` |
| Go coverage | Pass | 80.8% of project statements from `go test -coverprofile=coverage.out ./...`; generated Go code inside `web/node_modules` is excluded from the project floor |
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
| Rolling source isolation | Pass on macOS | BlackPearl's runtime image and container contain no MP4/MKV and receive no source mount; the complete generated fixture exists only in the separate range-origin container |
| Rolling logical file | Pass on macOS | Plex indexed and played the 3,417,699-byte logical MP4 while BlackPearl's configured hard cache quota was 1,048,576 bytes |
| Rolling exact random reads | Pass on macOS | Plex ranges at offsets 0, 1,310,720, and 3,145,728 matched the range-origin bytes exactly |
| Rolling quota and eviction | Pass on macOS | The acceptance script sampled published chunks plus in-flight fetch files throughout a full stream, never exceeded 1 MiB, and observed an evicted range being fetched again after restart |
| Rolling Plex client playback | Pass on macOS | Plex Web visibly played the generated test pattern from the rolling stack; the server logged `MDE=1000,Direct play OK` with `decision=direct play`, served all 3,417,699 original bytes, and recorded a playing timeline |
| TorBox API/CDN contract | Pass (mocked) | TLS contract tests cover account metadata, bearer/query authentication, strict non-sequential ranges, immutable validators, signed-link reuse/refresh, redirects, CDN size validation, cancellation, concurrency, and secret redaction |
| Browser-first TorBox stack | Pass without provider credentials on macOS | The launcher generated a private first-run pairing value; Docker image built with embedded Next UI; Compose started BlackPearl and Plex healthy with no TorBox token/object environment; Plex mounted the empty read-only NFS export; `/healthz` returned 200 and `/readyz` returned `setup_required` |
| Browser setup UI | Pass on macOS | The embedded production page consumed and removed the pairing fragment, hydrated without console warnings, displayed first-setup state, and passed desktop 1280x800 plus narrow 390x844 visual checks; API/component tests cover discovery, selection, ready, empty, and error states |
| Browser token persistence and activation | Pass in automated tests | Private file modes, bounded token reads, no response echo, CSRF/Origin/Host checks, runtime prepare/activate/reload, and rollback are covered under the race detector |
| Browser replacement transaction | Pass in automated tests | Token/config generations commit through one atomic pointer; failed publication restores the prior pointer; NFS publishes namespace and catalog together |
| NFS replacement handle stability | Pass in protocol and live restart tests | A real NFSv3 client keeps reading original bytes from an issued handle after replacement while a new mount reads the new catalog; deterministic handles resolve the current file after server recreation; the live Plex mount remained readable across a BlackPearl-only restart |
| Shared rolling quota during replacement | Pass in automated tests | Multiple immutable provider runtimes use one process-lifetime cache owner and one hard-quota ledger |
| Seek-aware rolling read-ahead | Pass in race tests | Configurable background chunks follow the latest foreground offset, coalesce with demand reads, continue after LRU saturation, protect the most recently demanded chunk, and preserve one-chunk foreground headroom. |
| TorBox live read-ahead | Pass on macOS | One previously uncached NFS read at byte 700,000,000 of the TV episode produced the demanded 1 MiB chunk plus all eight configured adjacent chunks (9/9 files, 9,437,184 bytes total) without storing the complete object. The known H.264/AAC movie remained `directplay` on the rebuilt stack. |
| Setup API container isolation | Pass in Compose, API, and live container tests | BlackPearl and Plex use disjoint Docker networks; Docker Desktop host-gateway reachability is treated as untrusted. A forged unpaired mutation from the live Plex container was denied with HTTP 401 and issued no session. First setup requires a host-generated pairing value; later mutations require a setup-origin session header, pairing value, or exact saved-token re-entry. No authorization cookie is sent to Plex. |
| TorBox live provider | Pass on macOS with an authorized account file | Live TorBox metadata and repeated exact ranges at the beginning, interior, midpoint, and tail passed through both the gateway and the rolling cache; no complete media file was stored locally |
| TorBox dynamic Plex namespace | Pass on macOS | A setup replacement changed the NFS namespace without restarting Plex; one-second attribute caching plus disabled negative lookup caching exposed the new path, and generation-based modification times caused Plex to rescan it |
| TorBox Plex Direct Play and seek | Pass on macOS | Plex indexed an authorized 1,783,163,131-byte H.264/AAC MP4, reported `decision="directplay"`, resumed at 10:13 after a non-sequential seek, and advanced continuously to 10:34 while BlackPearl held 56,519,784 bytes of rolling chunks |
| TorBox CDN request control | Pass in unit and live tests | A validated signed link is reused within its TTL instead of issuing a validation range before every NFS read; an expired link still refreshes on the first rejected content range. This removed live CDN 429 failures during Plex playback. |
| TorBox multi-item manifest | Pass on macOS | Browser setup searched 3,232 eligible account videos, atomically published two authorized logical MP4s, and Plex indexed both without a Plex or stack restart. Random reads succeeded in both files. |
| TorBox mixed movie/TV manifest | Pass on macOS | Browser setup published two movies plus one explicit TV episode. PearlNFS exposed canonical Movies and `TV Shows/Friends (1994)/Season 07` paths; Plex matched Friends S07E01 in a separate TV library. Beginning, midpoint, and tail reads succeeded for the 890,602,349-byte episode. |
| TorBox manifest restart recovery | Pass on macOS | After a BlackPearl-only restart, the saved three-item mixed manifest restored, both NFS roots remained readable through the existing Plex mount, and the rolling cache held about 208 MB for about 2.84 GB of logical media. |
| TorBox mixed playback decisions | Pass on macOS | The known H.264/AAC MP4 remained `directplay` after a seek from about 11:34 to 15:05. The MKV episode sought to 3:40 with original video copied and AC3 audio transcoded by Plex Web, confirming source-byte compatibility while preserving the Direct Play target for compatible media. |
| Cross-container Plex mount | Pending Ubuntu | Docker Desktop bind propagation cannot prove this Linux-host behavior |

The current result includes locally verified FUSE and portable NFS adapters, persistent and rolling macOS Plex playback, strict random-range retrieval, a live hard-quota test, eviction, and refetch evidence. The rolling client test completed the eight-second fixture; explicit rolling-client forward/backward seek evidence remains to be captured with a longer fixture. Windows and native-Linux portability remain unverified.

## Acceptance checklist

- [x] Range-oriented domain, core, cache, acquisition, and FUSE contracts do not expose local paths.
- [x] Persistent POC imports a legally usable generated fixture.
- [x] Rolling mode fetches exact missing ranges without requiring a complete local file.
- [x] Rolling cache accounts for in-flight fetch bytes and never exceeds its hard quota.
- [x] Concurrent misses coalesce and evicted chunks refetch correctly.
- [x] Race tests pass.
- [x] Coverage is at least 80%.
- [x] `go vet` passes.
- [x] Compose safety checks pass.
- [x] Kernel-mounted FUSE reads exact complete bytes and arbitrary offsets.
- [x] Containerized fixture has the intended H.264/AAC profile.
- [x] Portable Docker profile mounts PearlNFS into an unmodified Plex image.
- [x] Plex scans the fixture through PearlNFS and serves arbitrary source ranges unchanged.
- [x] A macOS Plex Web client Direct Plays the fixture and manual seeks succeed.
- [x] A macOS Plex Web client plays the rolling logical fixture and Plex records a Direct Play decision.
- [x] The TorBox profile starts without credentials and exposes an interactive localhost-only setup page.
- [x] Plex mounts an empty TorBox NFS export while BlackPearl reports setup-required media readiness.
- [x] An authorized TorBox token discovers account media through the live provider.
- [x] Plex Direct Plays and seeks an authorized TorBox-backed logical file.
- [x] A multi-item TorBox manifest publishes atomically, scans in Plex, and survives BlackPearl restart.
- [x] A mixed movie/TV manifest publishes canonical Plex paths, scans in separate libraries, seeks, and survives BlackPearl restart.
- [ ] A longer rolling fixture demonstrates explicit forward and backward client seeks before playback completes.
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
