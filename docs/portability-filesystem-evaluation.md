# Portable Plex filesystem evaluation

Date: 2026-08-13

## Decision

Add a read-only NFS frontend to the BlackPearl binary as the default portable
Plex transport. Keep the existing FUSE frontend as a native-Linux option.
Both frontends must adapt the same catalog and `domain.ReadHandle.ReadAt`
boundary; neither owns acquisition or cache policy.

The portable Compose profile should:

1. run BlackPearl's NFS server on one fixed TCP port;
2. publish that port only on the Docker daemon's loopback;
3. define a named volume using Docker's built-in `local` driver with NFSv3
   options;
4. mount the named volume read-only into the unmodified Plex container; and
5. wait for BlackPearl's NFS readiness check before starting Plex.

```text
Plex container
  /blackpearl (ordinary read-only files)
          |
Docker local NFS volume (mounted by the Docker daemon)
          |
127.0.0.1:<configurable-port>
          |
BlackPearl binary
  NFS adapter -> Catalog -> ReadHandle.ReadAt -> PearlCache -> RangeSource
                  |                              |             |
                  +-> logical size              +-> rolling   +-> provider
                                                  or persistent
```

This removes sibling mount propagation. Plex receives ordinary seekable file
semantics, while BlackPearl can advertise a logical size and fetch only the
ranges requested.

## Evidence collected

All local runtime experiments were executed on Docker Desktop for macOS with a
Linux ARM64 daemon. No production media paths or Plex installation were used.

### Docker local NFS volume

A read-only NFSv3 server exported the generated Milestone 1 MP4 and a sparse
one-terabyte file. A Docker `local` volume mounted the export using:

```text
type=nfs
device=:/
addr=127.0.0.1,vers=3,tcp,nolock,port=20491,mountport=20491,ro
```

An unprivileged Debian container:

- saw the MP4's exact 3,417,699-byte size;
- saw the logical file size as 1,099,511,627,776 bytes;
- returned the expected hash for a 64 KiB offset read; and
- could not write to the mount.

The same volume was mounted into the unmodified official Plex image. A running
Plex server scanned the NFS path and indexed the fixture as an 8-second,
1280x720 H.264/AAC MP4. Plex's original-media endpoint answered an arbitrary
64 KiB request with HTTP 206 and the exact source bytes.

That proves the scan and seekable original-byte path. It is Direct Play
compatible, but it does not prove that every Plex client will select Direct
Play: that decision still depends on the client's codec, container, bitrate,
and resolution support. The fixture format is deliberately H.264/AAC MP4.

### Direct range-to-NFS adapter

The Go experiment in `experiments/portability/readat-nfs` advertises a one-
terabyte logical file without storing it. The NFS implementation calls a file's
`ReadAt` with the requested offset. A client mounted the export through a
Docker local NFS volume and read the final four logical bytes correctly:

```text
offset: 1,099,511,627,772
bytes:  252 253 254 255
```

This proves the protocol does not impose a complete-local-file requirement.
The selected Go NFS library describes itself as minimally tested, so the spike
is architectural evidence, not a production dependency decision.

### SMB/CIFS

A read-only Samba share mounted through Docker's `local` CIFS volume also
passed file-size, offset-hash, ffprobe, and seek probes. The unmodified official
Plex image could read it without extra privileges.

SMB is a good transport but a poor initial BlackPearl adapter. Samba naturally
exports a local filesystem tree. Preserving `ReadAt` without materializing the
complete file would require FUSE inside the BlackPearl/Samba container, a
custom Samba VFS module, or a separate production-grade SMB server library.
That is substantially more integration work than the direct NFS `READ` seam.

### WebDAV

The WebDAV server advertised the one-terabyte logical length and returned exact
HTTP range responses. After mounting it with rclone FUSE, ffprobe and an ffmpeg
seek succeeded.

Plex library paths are filesystem paths, not WebDAV URLs. Giving Plex a WebDAV
mount therefore requires a custom Plex image or volume plugin plus FUSE,
`/dev/fuse`, and elevated mount privileges. It does not remove the original
portability problem.

### Mount inside or alongside Plex

The official Plex image has `mount` but does not include NFS or CIFS helpers. A
thin derivative image with `nfs-common` mounted the test export successfully
when run privileged. This works, but changes the Plex image and gives the Plex
container mount privileges.

Putting Plex and BlackPearl FUSE in one container would also share a mount
namespace, but couples release lifecycles and expands Plex's privileges.
Joining Plex's mount namespace from a sidecar has the same privilege and
orchestration problems. A named volume shared between sibling containers does
not solve propagation because Docker volumes use private propagation.

### Docker managed volume plugin

A managed FUSE volume plugin can present remote storage as a named volume, but
it requires a daemon-level plugin installation outside Compose and typically
needs host networking, `/dev/fuse`, and `CAP_SYS_ADMIN`. It is less portable and
has a larger privilege footprint than Docker's built-in NFS/CIFS support.

### Other abstractions

| Option | Why it is not the primary path |
|---|---|
| HTTP Range | Correct byte semantics, but Plex cannot scan an HTTP URL as a normal library filesystem. |
| SFTP/SSHFS/rclone mount | Requires FUSE in Plex or a privileged plugin. |
| 9p/VirtioFS | Coupled to VM/runtime internals rather than portable Compose. |
| NBD/block device | Requires maintaining a filesystem image and does not map cleanly to a dynamic media catalog. |
| Plex `.strm` or custom client | Does not satisfy the normal-file/no-custom-client requirement. |

## Comparison

Legend: **yes** is demonstrated or inherent; **conditional** needs the stated
adapter or deployment constraint; **no** fails a critical requirement.

| Architecture | Normal seekable files | Direct `ReadAt` seam | No full local file | Portable Compose | Unmodified Plex | Result |
|---|---:|---:|---:|---:|---:|---|
| Current FUSE + propagated bind mount | yes | yes | yes | no on Docker Desktop | yes | Keep for native Linux only |
| NFS + Docker local volume | yes | yes | yes | yes in macOS test | yes | **Recommended** |
| SMB + Docker local volume | yes | conditional | conditional | yes in macOS test | yes | Fallback transport |
| WebDAV + FUSE mount | yes after mount | yes | yes | conditional/privileged | custom image | Reject as default |
| Managed Docker volume plugin | yes | conditional | yes | daemon install required | yes | Reject as default |
| Mount NFS inside Plex | yes | yes | yes | privileged/custom image | no | Operational fallback |
| Co-locate Plex + FUSE | yes | yes | yes | one privileged image | no | Reject |
| HTTP Range alone | no | yes | yes | yes | yes | Reject for Plex library |

## Platform assessment

| Platform | Status | Evidence or remaining gate |
|---|---|---|
| macOS Docker Desktop | Confirmed | Compose NFS-volume probe, official Plex scan, and original-media range response passed. |
| Windows Docker Desktop, Linux containers | Architecturally compatible; unconfirmed | The mount occurs in Docker Desktop's Linux daemon/WSL2 environment, not on the Windows host. Run the acceptance suite on a Windows machine before claiming support. |
| Native Linux Docker | Confirmed server-side on Ubuntu 24.04 AMD64 | A disposable hosted run mounted PearlFS into the official Plex container, indexed the fixture, matched a non-sequential Plex-served range, logged `Direct play OK`, and verified clean unmount. A human-visible remote client session was outside that runner's scope. |

Windows remains deliberately unconfirmed until it has Docker Desktop runtime
evidence. Native Linux has server-side runtime evidence; its remaining client
gate is kept distinct rather than inferred from the Plex decision log.

## Production design constraints

The NFS frontend must not leak transport concerns into core or cache code.
Implementation should preserve these rules:

- A catalog entry reports immutable identity, path, attributes, and logical
  size. Replacing media creates a new version/file handle rather than changing
  bytes behind a cached NFS identity.
- NFS `READ(offset, count)` opens or reuses a `domain.ReadHandle` and calls
  `ReadAt(ctx, destination, offset)`.
- The rolling cache stores only configured ranges. Active reads pin ranges;
  misses coalesce; read-ahead is bounded by the byte quota.
- A requested range blocks until available or returns a bounded I/O failure.
  The exact NFS timeout/retry policy needs failure-mode tests before release.
- Exports remain read-only. The portable Compose profile binds NFS to daemon
  loopback, not the LAN.
- File handles must be stable across BlackPearl restarts and derived from
  durable media identity, not process memory.
- Metrics must distinguish NFS requests, cache hits, provider fetches,
  backpressure, timeouts, and eviction.
- The NFS port and volume name are configurable. Docker volume options are
  effectively deployment configuration; changing them requires recreating the
  named volume, never a media directory.

## Refactor gate

Do not replace PearlFS. The next implementation step is an additive
`PearlNFS` adapter and a portable Compose profile behind tests. Before calling
that profile supported, acceptance must cover:

1. catalog scan in an unmodified official Plex container;
2. exact-byte sequential playback and multiple non-monotonic range reads;
3. seek to near the end of a logical file whose full bytes are not local;
4. Plex Direct Play observed from at least one representative client session;
5. rolling-cache quota enforcement while playback and seeking continue;
6. BlackPearl restart, provider timeout, and cache-pressure failure behavior;
7. clean start/stop/recreate lifecycle in Compose; and
8. the same suite on macOS Docker Desktop, Windows Docker Desktop, and native
   Linux Docker.

Only after those gates should the portable profile become the documented
default.

## Primary references

- Docker documents that bind-propagation configuration is Linux-host only and
  does not work with Docker Desktop:
  https://docs.docker.com/engine/storage/bind-mounts/#configure-bind-propagation
- Docker documents that volumes use `rprivate` propagation and gives built-in
  NFS and CIFS `local` volume examples:
  https://docs.docker.com/engine/storage/volumes/
- Plex documents adding mounted network resources as library folders:
  https://support.plex.tv/articles/201122318-mounting-network-resources/
- Plex defines Direct Play as sending the source unchanged and notes that the
  client determines compatibility:
  https://support.plex.tv/articles/200250387-streaming-media-direct-play-and-direct-stream/
- Docker Desktop documents that its WSL2 backend runs Linux workspaces and
  Linux containers in the WSL environment:
  https://docs.docker.com/desktop/features/wsl/
- Docker documents managed plugin installation and lifecycle:
  https://docs.docker.com/engine/extend/
- The NFS protocol spike uses the go-nfs project, whose README labels it
  minimally tested:
  https://github.com/willscott/go-nfs
- rclone's NFS server is documented as experimental and exposes VFS cache
  modes suitable for read-only serving:
  https://rclone.org/commands/rclone_serve_nfs/
