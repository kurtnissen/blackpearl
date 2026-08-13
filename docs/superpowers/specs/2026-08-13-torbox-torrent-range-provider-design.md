# TorBox Torrent Range Provider Design

## Goal

Add TorBox as BlackPearl's first production-shaped acquisition provider while
preserving the existing `RangeSource` and rolling-cache contracts. A configured
file from an already-complete torrent in the user's TorBox account must appear
to PearlFS/PearlNFS as an immutable, seekable logical object. BlackPearl must
fetch only requested ranges and must never persist the TorBox API token or a
short-lived CDN URL.

This milestone is read-only. It supports media the user is authorized to access
and that already exists in the user's TorBox account. It does not search public
indexes, add magnets, create downloads, select files, or delete account items.

## Selected approach

BlackPearl will add a dedicated `gateway/torbox` adapter rather than teaching the
generic HTTP origin gateway about TorBox. The adapter owns TorBox authentication,
API response mapping, download-link renewal, and CDN validation. It implements
the same consumer-owned `cache.RangeOpener` boundary as the existing strict HTTP
gateway.

This keeps the dependency flow unchanged:

```text
PearlFS or PearlNFS
        |
        v
core.Catalog -> cache.RollingSource -> cache.RangeOpener
                                             |
                               +-------------+-------------+
                               |                           |
                        httporigin.Gateway           torbox.Gateway
                                                               |
                                             TorBox API -> TorBox CDN ranges
```

The same BlackPearl binary selects the provider through configuration. No TorBox
type crosses into core, the filesystem adapters, SQLite, or cache policy.

## Scope and object identity

The provider name is `torbox-torrent`. A backing object ID uses the canonical
form:

```text
<torrent-id>:<file-id>
```

Both components are positive base-10 integers with no signs, whitespace,
leading path characters, or extra separators. The object ID contains no API
token, filename, magnet, torrent hash, or CDN URL.

The first runtime integration continues to register the configured object as
the isolated POC movie. This is enough to prove the provider/storage path without
introducing general discovery or catalog-management behavior. A later resolver
milestone will register normalized TorBox titles and paths.

## Configuration

Rolling mode gains a provider selector:

```text
BLACKPEARL_RANGE_PROVIDER=http-range|torbox-torrent
```

`http-range` remains the default so the existing macOS proof is unchanged.

TorBox configuration:

```text
BLACKPEARL_TORBOX_API_URL=https://api.torbox.app/v1/api/
BLACKPEARL_TORBOX_API_TOKEN=<secret>
BLACKPEARL_RANGE_OBJECT_ID=<torrent-id>:<file-id>
```

The API URL defaults to TorBox's official HTTPS endpoint. Non-HTTPS TorBox URLs
are rejected except in unit/integration tests where an explicit constructor may
receive a local HTTP test server. Production environment parsing requires HTTPS.
The token is required only when `torbox-torrent` is selected and is rejected if
blank or surrounded by whitespace.

The token remains process memory only. It is never written to SQLite, filenames,
cache keys, logs, readiness responses, metrics, or errors. TorBox's `requestdl`
API requires the token query parameter; BlackPearl sends it only to the configured
TorBox API host and never forwards it to the returned CDN URL.

## TorBox API flow

Opening `<torrent-id>:<file-id>` performs the following logical operations:

1. Request `GET torrents/mylist?id=<torrent-id>&bypass_cache=true` with
   `Authorization: Bearer <token>`.
2. Require HTTP 200, `success=true`, one matching torrent, and one matching file.
3. Require `download_finished=true`, `download_present=true`, positive file size,
   `zipped=false`, and `infected=false`.
4. Derive an immutable validator from the strongest file metadata available:
   file hash, then MD5, plus the exact logical size. A file with no stable hash is
   rejected rather than cached ambiguously.
5. Request `GET torrents/requestdl` with `torrent_id`, `file_id`,
   `redirect=false`, `append_name=false`, and the required token parameter.
6. Require the standard TorBox envelope with `success=true` and an absolute HTTPS
   URL in `data`.
7. HEAD the CDN URL without TorBox authorization and require the expected positive
   `Content-Length` and byte-range support.

TorBox error bodies are bounded before decoding. User-facing `detail` text may be
included in wrapped errors after control-character removal and length limiting;
raw bodies, tokens, and download URLs are never included.

## Link caching and refresh

TorBox download links are short-lived and API calls are rate limited. The gateway
therefore caches resolved CDN links in memory only, keyed by canonical object ID
and immutable validator. The default link lifetime is two hours, safely below
TorBox's documented three-hour opening window.

Concurrent requests for the same object coalesce into one link-generation call.
An HTTP 401, 403, or 410 from the CDN invalidates the cached link, obtains one new
link, and retries the exact range once. Other failures are returned immediately.
The retry remains bounded by the caller's context and HTTP timeout.

Metadata is cached for sixty seconds to avoid one `mylist` request per rolling
chunk. After expiry, the next open refreshes metadata. If the validator changes,
the existing rolling-cache validator-scoped key naturally moves reads to a new
chunk namespace and prevents mixed-version files.

## Strict range behavior

The returned source implements:

```go
type RangeSource interface {
    ReadAt(context.Context, []byte, int64) (int, error)
    Size() int64
    Validator() string
    Close() error
}
```

Every CDN read sends one exact `Range: bytes=start-end` request. A valid response
must be HTTP 206, contain the exact expected `Content-Range`, and contain exactly
the requested bytes, except for normal EOF behavior on a final partial caller
buffer. HTTP 200 is rejected so a CDN cannot silently cause a complete download.

Redirects are disabled for TorBox API calls. CDN redirects are also disabled;
the URL returned in the signed API response must itself be directly range-capable.
This prevents credentials or requests from being redirected to an unexpected
host.

## Errors and lifecycle

All I/O accepts `context.Context` first. Network responses and bodies are closed
on every path, and close errors are joined with operation errors. Authentication
failures, missing account items, incomplete downloads, infected files, zipped
files, malformed envelopes, invalid CDN URLs, size mismatches, range mismatches,
and validator changes receive distinct contextual errors.

Closing a TorBox range source prevents later reads but does not revoke the shared
in-memory link. The gateway stores no bytes; PearlCache remains the sole owner of
rolling storage and eviction.

## Testing and acceptance

Implementation follows red-green-refactor with `httptest` contract servers.
Tests must prove:

- canonical object-ID parsing and rejection;
- bearer authentication only to the configured API host;
- token query use only on `requestdl`, with no secret in errors;
- completed-file metadata mapping and stable validator derivation;
- rejection of incomplete, missing, infected, zipped, zero-size, or hashless files;
- standard TorBox success and error envelope handling;
- HTTPS CDN URL enforcement;
- exact non-sequential reads and EOF behavior;
- rejection of HTTP 200, malformed `Content-Range`, and short/oversized bodies;
- metadata and link reuse within their TTLs;
- concurrent link-request coalescing;
- one refresh-and-retry after CDN 401, 403, or 410;
- cancellation and closed-source behavior;
- configuration validation and single-binary wiring; and
- all persistent, HTTP rolling, FUSE, NFS, Compose, and Plex regressions remain
  green.

A live TorBox check is opt-in and requires `BLACKPEARL_TORBOX_API_TOKEN` plus an
authorized `<torrent-id>:<file-id>`. Until those are supplied, passing mocked
contract tests means the integration is code-complete but not live-provider
validated. Documentation must preserve that distinction.

## Non-goals

- Public torrent or indexer search.
- Prowlarr integration.
- Creating, uploading, pausing, resuming, seeding, or deleting torrents.
- TorBox Usenet or web-download adapters.
- Automatic file selection or episode matching.
- General catalog import and metadata naming.
- Persisting or sharing TorBox CDN links.
- Read-ahead, next-episode prefetch, or bandwidth scheduling.
