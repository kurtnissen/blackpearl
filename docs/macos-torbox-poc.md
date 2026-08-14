# macOS TorBox Plex POC

This profile connects BlackPearl's rolling range cache to a selected manifest
of MP4/MKV files in your TorBox account, exposes them through PearlNFS, and
lets an isolated Plex container scan and Direct Play the logical files. It also
includes Prowlarr so BlackPearl can search indexers you are authorized to use
and add a result only when TorBox reports it is already cached. BlackPearl does
not submit uncached downloads through this flow.

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

Prowlarr is available only on the Mac at `http://localhost:9697`. Complete its
one-time setup, add only indexers you are authorized to use, then copy the API
key from **Settings → General**. In BlackPearl's ready screen, select **Find
something new**. The default internal URL is `http://prowlarr:9696`; paste the
Prowlarr API key and select **Connect Prowlarr**. Movie and TV episode requests
then use the configured indexers, deterministic release ranking, and a strict
TorBox cached-only check before the Plex manifest changes.

This profile also mounts its own `plex-config` named volume into BlackPearl at
`/plex-config` read-only. Once Plex sign-in has created `Preferences.xml`,
BlackPearl reads the current account token on demand and observes the Plex
Watchlist every 15 minutes. The token is sent only in the `X-Plex-Token` header
to the bounded watchlist adapter; it is not copied into BlackPearl state or
returned by the API. The paired `/api/watchlist/status` route returns only
health, last-sync time, and aggregate queue counts—never titles or Plex IDs.

Observation is enabled by default but cannot mutate Prowlarr, TorBox, Plex's
Watchlist, or the media manifest. After Prowlarr authentication and authorized
indexers are configured, opt in to serialized cached-only movie processing by
setting this before launch:

```bash
BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED=true ./scripts/torbox-stack.sh start
```

Uncached movies wait six hours before another check. Known transient failures
wait 15 minutes. Any provider or publication mutation with an ambiguous result
moves to manual review instead of being retried. Watchlisted shows are counted
but never acquired because a show alone does not specify season or episode.

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

The Prowlarr endpoint and API key are stored separately at
`/var/lib/blackpearl/setup/acquisition/search-provider.json` with a private
directory and `0600` file mode. The acquisition status API returns only whether
this connection exists. Neither credential is returned to the browser after it
is saved.

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
through 64. Foreground reads keep priority and a seek moves the window. Opening
a TV episode also prefetches the first sixteen 1 MiB chunks of the next episode
in the same show by default. Set `BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS=0` to
disable this or choose a prefix from 1 through 256 chunks. This background work
uses the same hard quota, retains foreground headroom, stops instead of evicting
current cache data, and never downloads the
whole next episode unless the configured prefix itself spans the whole file.

If TorBox is briefly unavailable while BlackPearl starts, saved setup restore
retries with bounded exponential backoff. The setup page remains available, and
the existing manifest is republished automatically when the provider recovers.

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
- Prowlarr Web: `http://localhost:9697`

`/healthz` passes while setup is incomplete so Plex can mount the empty NFS
export. `/readyz` reports `setup_required` until a selection is active.

Live discovery, Plex scanning, Direct Play, seeking, restart recovery, and
bounded next-episode prefetch are
separate acceptance evidence. They were observed on macOS on 2026-08-14 with a
mixed four-video authorized manifest: Plex indexed two movies and matched two
episodes in a separate TV library, an H.264/AAC MP4 remained Direct Play through
a non-sequential seek, the first episode played with its original video stream,
and BlackPearl restored both library roots after restart. Opening only episode
one populated exactly the configured 16 MiB prefix of episode two and did not
store its tail or complete object.

The Watchlist gateway, durable queue, observe-only process wiring, and
serialized cached-only worker are covered by mocked full-process tests. Live
observe-only counts and a live Watchlist-triggered provider mutation remain
separate acceptance gates.
