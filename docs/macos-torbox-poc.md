# macOS TorBox Plex POC

This profile connects BlackPearl's rolling range cache to one already-complete
MP4 or MKV in your TorBox account, exposes it through PearlNFS, and lets an
isolated Plex container scan and Direct Play the logical file. It does not
search for, create, alter, or delete TorBox downloads.

## Start it

Use only media you are authorized to access. No token or file ID is needed in
the terminal:

```bash
docker compose -f compose.torbox.yaml up -d --build --wait
open http://localhost:8082
```

The setup page is published only on the Mac's loopback interface. Paste your
TorBox API token and select **Find my videos**. BlackPearl shows completed,
present, unarchived, uninfected MP4/MKV files. Choose one, adjust the Plex title
or year if needed, and select **Use with Plex**.

BlackPearl stores the token only at `/var/lib/blackpearl/setup/torbox.token`
inside its named data volume with mode `0600`. The setup directory uses mode
`0700`. The token is never returned by the API, placed in browser storage,
container environment, SQLite, logs, telemetry, or cache filenames.

## Add it to Plex

Open `http://localhost:32402/web`. Complete Plex's normal sign-in if this is a
new isolated server, then create one Movies library rooted at:

```text
/blackpearl/Movies
```

Scan the library after BlackPearl reports **ready**. The selected path uses its
real `.mp4` or `.mkv` extension and TorBox logical size. Seeking and Direct Play
read arbitrary ranges through PearlNFS; the complete file is not required on
BlackPearl's disk.

The default cache limit is 40 GiB. Override it before launch with, for example,
`BLACKPEARL_CACHE_MAX_BYTES=4294967296` for 4 GiB. The logical file may be
larger than this limit; BlackPearl stores only requested fixed-size chunks and
evicts old chunks as needed.

## Inspect and stop

```bash
docker compose -f compose.torbox.yaml ps
docker compose -f compose.torbox.yaml logs blackpearl
docker compose -f compose.torbox.yaml down
```

Add `-v` only when you intentionally want to delete this POC's isolated token,
selection, catalog, rolling cache, and Plex configuration volumes.

Ports are isolated from the existing POCs:

- BlackPearl setup: `http://localhost:8082`
- BlackPearl health: `http://localhost:8082/healthz`
- BlackPearl media readiness: `http://localhost:8082/readyz`
- PearlNFS: `localhost:20492`
- Plex Web: `http://localhost:32402/web`

`/healthz` passes while setup is incomplete so Plex can mount the empty NFS
export. `/readyz` reports `setup_required` until a selection is active.

Live discovery, Plex scanning, Direct Play, and seeking are separate acceptance
evidence. Do not call them passed until each has been observed with an
authorized account file.
