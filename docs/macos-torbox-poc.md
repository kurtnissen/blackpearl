# macOS TorBox Plex POC

This profile connects BlackPearl's rolling range cache to a selected manifest
of already-complete MP4/MKV files in your TorBox account, exposes them through
PearlNFS, and lets an isolated Plex container scan and Direct Play the logical files. It does not
search for, create, alter, or delete TorBox downloads.

## Start it

Use only media you are authorized to access. No token or file ID is needed in
the terminal:

```bash
./scripts/torbox-stack.sh start
```

The setup page is published only on the Mac's loopback interface. Paste your
TorBox API token and select **Find my videos**. BlackPearl shows completed,
present, unarchived, uninfected MP4/MKV files. Search and select up to 100,
choose Movie or TV episode for each item, adjust the editable Plex metadata if
needed, and select **Use with Plex**. Conventional `SxxEyy` filenames receive
an editable TV suggestion; filename parsing is never authoritative.

The launcher creates a private local pairing value under the ignored
`runtime/` directory and carries it in the setup page URL fragment. The
fragment is removed before any request, is never sent to Plex, and authorizes
first setup through a dedicated request header. BlackPearl stores the TorBox
token only in the active private generation beneath
`/var/lib/blackpearl/setup/generations/`, with mode `0600`. An atomically
replaced `current` pointer commits the token and complete manifest as one pair.
Legacy single-selection state is loaded as a one-item manifest. The
setup directories use mode `0700`; inactive and orphan generations are
removed on startup and after a successful replacement. The TorBox token is
never returned by the API, placed in browser storage, container environment,
SQLite, logs, telemetry, or cache filenames. The setup page stores only a
derived session authorization and local pairing value in that page's
port-scoped `sessionStorage`; neither value is sent to Plex.

## Add it to Plex

Open `http://localhost:32402/web`. Complete Plex's normal sign-in if this is a
new isolated server, then create a Movies library rooted at:

```text
/blackpearl/Movies
```

For selected episodes, also create a TV Shows library rooted at:

```text
/blackpearl/TV Shows
```

Scan the library after BlackPearl reports **ready**. Each selected path uses its
real `.mp4` or `.mkv` extension and TorBox logical size. Seeking and Direct Play
read arbitrary ranges through PearlNFS; the complete file is not required on
BlackPearl's disk.

Direct Play is determined by the selected file and Plex client. H.264/AAC MP4
is the most broadly compatible browser profile. HEVC, Dolby Vision, AC3, and
some MKV combinations may make Plex remux or transcode even though BlackPearl
still serves the original source bytes. BlackPearl does not transcode.

The default cache limit is 40 GiB. Override it before launch with, for example,
`BLACKPEARL_CACHE_MAX_BYTES=4294967296` for 4 GiB. The logical file may be
larger than this limit; BlackPearl stores only requested fixed-size chunks and
evicts old chunks as needed. The TorBox profile reads ahead eight 1 MiB chunks
from the latest Plex range by default. Set
`BLACKPEARL_CACHE_READ_AHEAD_CHUNKS=0` to disable it or choose a value from 1
through 64. Foreground reads keep priority and a seek moves the window.

## Inspect and stop

```bash
./scripts/torbox-stack.sh status
./scripts/torbox-stack.sh logs
./scripts/torbox-stack.sh stop
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

Live discovery, Plex scanning, Direct Play, seeking, and restart recovery are
separate acceptance evidence. They were observed on macOS on 2026-08-14 with a
mixed three-video authorized manifest: Plex indexed two movies and matched one
episode in a separate TV library, an H.264/AAC MP4 remained Direct Play through
a non-sequential seek, the episode resumed after a seek, and BlackPearl restored
both library roots after restart. The logical manifest was about 2.84 GB while
the rolling cache held about 208 MB.
