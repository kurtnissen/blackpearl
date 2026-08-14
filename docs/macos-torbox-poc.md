# macOS TorBox Plex POC

This profile connects BlackPearl's rolling range cache to a selected manifest
of MP4/MKV files in your TorBox account, exposes them through PearlNFS, and
lets an isolated Plex container scan and Direct Play the logical files. It also
includes Prowlarr so BlackPearl can search indexers you are authorized to use
and adds a bounded public Internet Archive search adapter for legally
redistributable POC media. A cached match can publish immediately. An uncached
match advances as a durable background job: BlackPearl prefers an exact
licensed range file when available and uses **Prepare through TorBox** only for
the uncached-torrent fallback.

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

The Compose profile enables BlackPearl's direct open-media search adapter.
BlackPearl combines public Archive and configured Prowlarr results and tolerates
a failure from either source when the other succeeds. Archive movie search uses
the record's structured year rather than requiring the year to appear in
free-form title text, and normalizes that verified year into the candidate
title. Complete title/year or title/episode matching remains an eligibility
boundary, and auxiliary trailers, teasers, samples, previews, and featurettes
are removed before cache ordering. BlackPearl cache-probes up to 100 eligible
unique hashes, moves cached releases first, and only then persists at most five
deduplicated torrent candidates. Archive torrent metadata is downloaded only
from the selected Archive item, followed only across trusted Archive HTTPS
hosts, bounded to 4 MiB, and verified against the selected BitTorrent info hash
before TorBox receives it. This avoids depending on peer metadata discovery
while preserving the same provider-neutral job boundary.

For exact licensed Archive MP4/MKV files, BlackPearl persists an opaque
provider/object reference instead of a URL. Preparation validates only metadata,
then Plex reads arbitrary offsets through the common rolling or persistent
cache. The five-candidate durable plan orders cached torrents first, exact range
files second, and uncached torrents last while reserving a direct slot. Direct
objects are read-only external sources and are never deleted by BlackPearl.

If the top-ranked release is not cached, the page offers **Prepare through
TorBox**. BlackPearl persists only the request, up to five locator-free release
fingerprints, candidate outcomes, provider object ID, ownership provenance, and
state transitions in SQLite—never a token, magnet, signed URL, or torrent
payload. Cached eligible candidates are tried first while preserving resolver
rank within cached and uncached groups. The worker reconciles each hash before
creating, survives process restarts, polls only the attached object, publishes
only an eligible MP4/MKV, and refreshes Plex after the manifest transaction. If
an owned object stalls, disappears, or has no playable media, BlackPearl
deletes that exact object once and atomically advances. It never deletes an
existing account object, and uncertain cleanup stops in manual review.

This profile also mounts its own `plex-config` named volume into BlackPearl at
`/plex-config` read-only. Once Plex sign-in has created `Preferences.xml`,
BlackPearl reads the current account token on demand and observes the Plex
Watchlist every 15 minutes. The token is sent only in the `X-Plex-Token` header
to the bounded watchlist adapter; it is not copied into BlackPearl state or
returned by the API. The paired `/api/watchlist/status` route returns only
health, last-sync time, and aggregate queue counts—never titles or Plex IDs.

Observation is enabled by default but cannot mutate Prowlarr, TorBox, Plex's
Watchlist, or the media manifest. After Prowlarr authentication and authorized
indexers are configured, opt in to restart-safe movie preparation by setting
The environment value below is an optional first-start seed:

```bash
BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED=true ./scripts/torbox-stack.sh start
```

After startup, the paired setup page's **Turn automatic adding on/off** control
is authoritative and survives restart. The opt-in allows TorBox to download the
selected release when it is not cached, so use it only with authorized sources.
Items already in the Watchlist remain observation-only, and only an item first
seen on a later enabled sync is eligible. Movies use their exact title and year.
Shows remain observation-only unless **Start new shows with S01E01** is enabled
beneath the master control; that durable policy converts a newly observed show
into exactly one season 1 episode 1 request. It never requests a full season or
series, and enabling it is non-retroactive. Turning either control off blocks
matching new and retry claims without canceling provider work already preparing.
The Watchlist queue persists the exact movie/episode coordinates and durable
acquisition job ID and reconciles them across restarts. No-source or stalled
jobs wait six hours before another attempt. Known transient failures wait 15
minutes. Any provider or publication mutation with an ambiguous result moves to
manual review instead of being retried. Playback-triggered next-episode intent
is a later milestone and is intentionally separate from Watchlist membership.

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
mixed five-video authorized manifest: Plex indexed three movies and matched two
episodes in a separate TV library, an H.264/AAC MP4 remained Direct Play through
a non-sequential seek, the first episode played with its original video stream,
and BlackPearl restored both library roots after restart. Opening only episode
one populated exactly the configured 16 MiB prefix of episode two and did not
store its tail or complete object.

The direct-file acceptance used the licensed *MariposaHD* S01E01 MP4. A durable
episode job published the canonical TV path with a 175,099,607-byte logical
size. Start, interior, and tail 64 KiB reads matched the legal origin. Plex
indexed it as metadata ID 24 and logged `MDE=1000,Direct play OK`; Brave played
continuously, sought forward and backward, then resumed after only BlackPearl
was restarted. The restored midpoint 1 MiB range matched SHA-256
`34b458cdf8cbfcba06230e9f42790aecbfaaae143e728279693e46181602dae9`.
Only 34 chunks totaling 34,808 KiB existed for this 167 MiB logical object.
Plex itself was not restarted or modified.

The first live acquisition-console acceptance used Prowlarr's public Internet
Archive indexer and Blender's Creative Commons-licensed *Big Buck Bunny (2008)*.
The exact Internet Archive torrent was first made available in the authorized
TorBox account because BlackPearl intentionally refuses uncached downloads.
BlackPearl then performed its normal read-only search, cached check, account
object inspection, atomic five-item manifest publication, and automatic Plex
refresh. Plex indexed the 104,040,028-byte H.264/AAC MP4, Direct Played it, and
remained active through forward and backward seeks. This proves the live
cached-acquisition path.

The durable-acquisition acceptance then used the direct open-media adapter with
Blender's Creative Commons-licensed *Tears of Steel (2012)*. The result was not
in TorBox's cache. BlackPearl selected the exact 2012 Archive item, downloaded
and info-hash-verified its bounded `.torrent` metadata, created one TorBox
object with uncached preparation explicitly allowed, and observed the provider
complete a 381,008,624-byte release. A durable replay reconciled that object and
reached `succeeded`, exercising automatic publication through the background
worker. The resulting 190,489,483-byte H.264/AAC MP4 expanded the live manifest
to six items and Plex indexed metadata ID 14. Brave played it with
`decision="directplay"`, no transcode session, moved forward to 1:58, back to
1:49, and was paused. BlackPearl held 57,320,331 bytes across 56 requested
1 MiB chunks, including the tail chunk, rather than the complete logical file.
Two additional legal public torrents stopped at TorBox's explicit no-seed
state and were not published; their exact test objects were removed.

The Watchlist gateway, durable linked-job queue, observe-only process wiring,
and serialized durable handoff are covered by full-process race tests. Live
acceptance added the public-domain *House on Haunted Hill (1959)* after the
startup baseline. BlackPearl attached a durable acquisition job, safely fell
through unavailable releases, published a 197,597,484-byte MP4, refreshed the
movie library, and reached `succeeded`. Plex indexed metadata ID 17, Brave
played it with `decision="directplay"` and no transcode session, and forward and
reverse seeks succeeded. Start, midpoint, and tail NFS reads were exact; after
a BlackPearl rebuild the same logical size and midpoint hash returned and Plex
resumed the paused Direct Play session. The final rolling cache held 46 files
and 46,540 KiB for this 197,597,484-byte logical movie, not a complete local
copy. Temporary acceptance Watchlist entries were removed afterward while the
three pre-existing entries were preserved.
