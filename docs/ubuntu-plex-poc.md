# Ubuntu Server Plex POC

This runbook proves the final Milestone 1 user journey on an Ubuntu Server: Plex scans and plays a synthetic video through a PearlFS FUSE mount. It creates an isolated Plex configuration under this repository. It does not reuse or modify an existing Plex installation.

## Prerequisites

- Ubuntu Server with Docker Engine and the Docker Compose plugin.
- `/dev/fuse` available on the host.
- `curl`, `jq`, `findmnt`, `mountpoint`, and `sudo`.
- TCP 32400 reachable from the browser used for Plex setup; TCP 8080 is used for BlackPearl diagnostics.
- A Plex account and a temporary claim token if the new server cannot be claimed interactively.

Clone the repository somewhere with enough room for the generated Plex configuration. Run all commands from the repository root.

## 1. Inspect the isolation boundary

The supplied Compose files bind only these repository-owned paths:

```text
runtime/data
runtime/mount
runtime/plex-config
runtime/transcode
```

Verify that `compose.yaml` and `compose.poc.yaml` have not been edited to reference production paths:

```bash
./scripts/test-compose-paths.sh
```

## 2. Configure the isolated Plex server

```bash
cp .env.example .env
```

Edit `.env`:

- set `PLEX_ADVERTISE_IP` to `http://SERVER_IP:32400/`;
- set `TZ` as desired;
- optionally paste a fresh token from `https://plex.tv/claim` into `PLEX_CLAIM` immediately before first startup.

The `.env` file is ignored by Git. BlackPearl does not persist or log the claim token.

## 3. Prepare mount propagation

```bash
./scripts/prepare-ubuntu-poc.sh
```

The script confirms Linux and `/dev/fuse`, creates only the four runtime directories, bind-mounts `runtime/mount` onto itself, and marks it shared. This allows BlackPearl's nested FUSE mount to propagate into the Plex container as a read-only slave mount.

## 4. Start the POC

```bash
docker compose -f compose.yaml -f compose.poc.yaml up --build -d
docker compose -f compose.yaml -f compose.poc.yaml ps
```

Wait until BlackPearl is healthy. The POC image generates its own 8-second test-pattern MP4 at build time and imports it into PearlCache during startup.

## 5. Verify FUSE before involving Plex

```bash
./scripts/verify-fuse.sh
docker compose -f compose.yaml -f compose.poc.yaml exec -T plex \
  test -r '/blackpearl/Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4'
```

The first check compares complete SHA-256 hashes and an 8 KiB non-sequential range through PearlFS. The second proves that the sibling Plex container sees the propagated mount read-only.

If the second command fails while the first passes, inspect propagation:

```bash
findmnt -no TARGET,PROPAGATION --target runtime/mount
docker compose -f compose.yaml -f compose.poc.yaml logs blackpearl
```

The host mount must report `shared`.

## 6. Scan and play in Plex

1. Open `http://SERVER_IP:32400/web`.
2. Finish claiming the isolated server if needed.
3. Add a Movies library named `BlackPearl POC`.
4. Select `/blackpearl/Movies` as its only folder.
5. Scan the library and confirm exactly one intended title: `BlackPearl POC (2026)`.
6. Start playback and seek near the end.
7. Open Plex Dashboard while playing and confirm the video is Direct Play. Record the actual audio decision as well; clients may vary.

Do not add another media folder. The POC does not need Sonarr, Radarr, Prowlarr, or any acquisition service.

## 7. Record evidence

Fill in the Ubuntu/Plex section in [acceptance-evidence.md](acceptance-evidence.md) with:

- Ubuntu release and CPU architecture;
- Docker and Compose versions;
- the Git commit tested;
- FUSE verification output;
- Plex container image tag;
- library scan result;
- Direct Play/transcode decision from the Plex dashboard; and
- seek result.

Milestone 1 is not Plex-accepted until that evidence is captured from a real Ubuntu host.

## 8. Stop or remove the isolated data

Preserve data:

```bash
./scripts/cleanup-poc.sh
```

Remove generated BlackPearl cache/database and isolated Plex config/transcode data:

```bash
./scripts/cleanup-poc.sh --remove-data
```

The cleanup script is guarded to this repository's exact `runtime/` root. The host bind mount is unmounted before data removal. Review the script before running it if the repository was moved or modified.
