# Portability experiments

These spikes answer one question: can Plex consume a seekable virtual media
library through Docker without propagating a FUSE mount between sibling
containers?

They are research fixtures, not production adapters. They do not modify the
Milestone 1 FUSE implementation.

## Reproduce the Docker NFS-volume path

Run from the repository root on Docker Desktop for macOS or a Linux Docker
host:

```sh
docker compose -f experiments/portability/nfs-volume/compose.yaml \
  up --build -d --wait nfs
docker compose -f experiments/portability/nfs-volume/compose.yaml \
  run --rm --no-deps consumer
docker compose -f experiments/portability/nfs-volume/compose.yaml \
  down -v --remove-orphans
```

The fixture creates:

- an MP4 test video in a Docker volume;
- a one-terabyte sparse logical file;
- a read-only NFSv3 export bound only to the Docker daemon's loopback; and
- a Docker `local` volume that mounts the export into an unprivileged consumer.

The consumer asserts the video size, logical file size, and SHA-256 of a 64 KiB
read beginning at offset 2 MiB.

## Reproduce the direct `ReadAt` NFS seam

```sh
cd experiments/portability/readat-nfs
go test -race ./...
go vet ./...
```

This Go spike advertises a one-terabyte file without creating that file. Its
`ReadAt` implementation generates only the requested byte range. The unit tests
read from the tail of the logical object and verify EOF behavior.

The server can also be built and placed behind the Docker local NFS volume:

```sh
docker build -t blackpearl/readat-nfs:experiment \
  experiments/portability/readat-nfs
```

The protocol implementation is intentionally minimal. It exists to prove that
NFS `READ` maps to BlackPearl's range-oriented boundary, not to select or bless
a production NFS library.

## Other transport spikes

- `smb/` contains the Samba image and read-only share configuration used to
  prove Docker's local CIFS volume can expose seekable media to an unmodified
  Plex image.
- `plex-nfs-client/` is a deliberately less-preferred fallback: a derivative
  Plex image with NFS mount helpers. It proves an in-container mount works, but
  it requires a custom image and elevated mount privileges.
- WebDAV was served with rclone and mounted with rclone FUSE. Range reads and
  media seeks worked, but the consumer required `/dev/fuse` and elevated
  privileges. That simply moves the portability problem into Plex.

Full measurements, limitations, and the architecture recommendation are in
`docs/portability-filesystem-evaluation.md`.
