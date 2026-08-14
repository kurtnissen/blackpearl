# On-demand acquisition foundation design

## Outcome

BlackPearl can ask configured, authorized search providers for a movie or TV
episode, normalize their releases, reject structurally unsafe results, and rank
usable candidates deterministically for a target playback profile. This slice
does not mutate TorBox or publish a new Plex item yet. It creates the narrow
boundary that the following cached-acquisition workflow will consume.

## Why this is next

The current system proves that Plex can scan, seek, and play complete TorBox
account objects through a quota-bounded range cache. The missing end-to-end
step is turning media intent into one of those range-readable objects. Putting
search normalization and selection policy in the resolver keeps Prowlarr,
TorBox, future Usenet, and future metadata/watchlist inputs out of PearlNFS and
PearlCache.

## Domain model

`acquisition.SearchRequest` represents media intent rather than a provider
object. It has an explicit kind:

- movie: title and release year;
- episode: show title, release year, season, and episode.

The request derives the literal query sent to search gateways. Titles are
trimmed, bounded to 200 UTF-8 bytes, and must not contain control characters.
Years use the existing 1888-2100 setup range; seasons are 0-99 and episodes are
1-999.

`acquisition.Release` is a normalized search result. It contains only data the
resolver needs: source-local ID, title, protocol, size, indexer name, optional
info hash, optional magnet URL, optional download URL, and optional seeders.
Protocols are `torrent` and `usenet`. URLs are validated absolute HTTP(S) URLs,
except magnets, which must use the `magnet` scheme and contain a BitTorrent
`xt=urn:btih:` value. Release data is ephemeral and is not returned to Plex or
persisted in setup generations.

Providers advertise narrow capabilities: supported protocols and whether they
can return hashes, magnets, or download URLs. Capabilities describe behavior;
they do not relax release validation.

## Search gateway

The first gateway is Prowlarr. It performs `GET /api/v1/search` with:

- `query` from the validated request;
- `type=search`;
- a bounded result limit;
- `X-Api-Key` authentication.

The configured base URL must be absolute HTTP(S), must not contain user info,
query, or fragment, and may include a path prefix. The API key is write-only
configuration and is never logged or included in errors. The gateway caps the
response body, rejects redirects, maps only torrent/usenet records, and skips
malformed individual releases. Authentication failures map to
`domain.ErrUnauthorized`; transport, decoding, and server failures stay
provider errors.

## Resolver behavior

Resolver search is best-effort across providers. A failed provider does not
discard valid releases from another provider. If every provider fails, the
resolver returns a joined error containing provider names without credentials
or release URLs. Identical releases are deduplicated by protocol plus a stable
identity: normalized info hash first, then source ID.

Ranking is deterministic and policy-only:

1. reject zero-size results and releases missing the retrieval locator required
   by their protocol;
2. prefer results whose normalized title contains the complete requested movie
   title or episode token;
3. prefer torrent results with an info hash, because TorBox cache availability
   can be checked before account mutation;
4. prefer higher seed counts for otherwise equivalent torrents;
5. prefer smaller releases only as the final playback-cost tie-breaker;
6. finish with provider name and source ID for stable ordering.

Quality and codec parsing are deliberately not guessed in this slice. A later
target-profile scorer will add container, video, audio, HDR, and language
preferences using reliable metadata or explicitly bounded title heuristics.

## Safety and boundaries

- Search is read-only. No download or account object is created in this slice.
- BlackPearl searches only user-configured providers and indexers.
- API keys never cross the gateway boundary or appear in structured release
  values.
- PearlNFS, PearlFS, and cache interfaces do not change.
- The existing browser-selected TorBox manifest remains fully functional.
- No production Plex, media, or `*arr` path is modified.

## Acceptance

- Domain tests reject malformed intent, locators, unsafe titles, and oversize
  inputs.
- Prowlarr TLS contract tests prove path-prefix handling, query encoding,
  authentication, response limits, cancellation, redirects, error mapping, and
  secret redaction.
- Resolver race tests prove best-effort fan-out, all-provider failure,
  deduplication, and stable ranking.
- Existing range-cache, TorBox, filesystem, setup, Compose, and live Plex paths
  remain unchanged and pass their regression suites.

## Next slice

The next implementation consumes the top ranked torrent with an info hash,
checks TorBox cache availability, creates it with `add_only_if_cached=true`,
polls bounded account state, selects the final MP4/MKV, and atomically appends or
replaces the Plex manifest. Uncached acquisition remains an explicit policy
choice rather than an accidental side effect.
