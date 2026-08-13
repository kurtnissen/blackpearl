# macOS Docker Desktop Plex POC

This profile runs BlackPearl and an isolated official Plex container without a
host FUSE mount, bind propagation, custom Plex image, or production media path.
Docker mounts BlackPearl's read-only NFS export into Plex as a normal named
volume.

## Start and verify

Requirements: Docker Desktop, Bash, curl, Python 3, and `shasum`.

```sh
./scripts/setup-portable-poc.sh
./scripts/verify-portable-poc.sh
```

The setup command builds the legal test-pattern video, starts the stack, creates
an isolated `BlackPearl POC` movie library, and waits for Plex to index it. The
verification command checks BlackPearl readiness, the NFS file visible inside
the official Plex image, the Plex catalog record, and an exact arbitrary HTTP
range returned by Plex.

On a fresh Plex configuration, `/identity` becomes available before Plex has
finished loading its library agents. The setup command waits through that
first-run phase, which can take roughly two minutes on Docker Desktop.

The setup command recreates both containers together. This gives Plex a fresh
NFS mount whenever the development BlackPearl image changes. Durable NFS file
handles across a BlackPearl-only restart remain a production-hardening gate;
the current POC does not claim restart-transparent playback.

No Plex claim token is needed for the initial local automated check. If you set
`PLEX_CLAIM` before startup or later claim the server, also export the resulting
`PLEX_TOKEN` when rerunning the setup or verification script.

## Play and seek

Open <http://localhost:32400/web>. Sign in and claim this isolated server if Plex
asks. Open the `BlackPearl POC` library and play `BlackPearl POC (2026)`.

During the eight-second test pattern:

1. seek from the beginning to about six seconds;
2. confirm playback resumes instead of restarting or failing; and
3. open Plex Dashboard, expand the active stream, and confirm `Direct Play` for
   both the H.264 video and AAC audio.

The automated exact-range check proves the unchanged-byte path. The dashboard
check is still required because Plex makes Direct Play decisions per client.

## Inspect or stop

```sh
docker compose -f compose.portable.yaml ps
docker compose -f compose.portable.yaml logs -f blackpearl plex
./scripts/cleanup-portable-poc.sh
```

The stop command preserves the isolated named volumes. To delete only the
portable POC's generated database, cache, Plex config, and transcode volumes:

```sh
./scripts/cleanup-portable-poc.sh --remove-data
```

The profile binds HTTP, NFS, and Plex only to macOS loopback. It does not search
for or mount a host Plex library, media directory, download directory, or
`*arr` path.
