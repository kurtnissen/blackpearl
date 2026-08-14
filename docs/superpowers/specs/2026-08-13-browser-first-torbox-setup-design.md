# Browser-First TorBox Setup Design

## Outcome

BlackPearl's TorBox profile starts without credentials, exposes a setup page only on the Docker host's loopback interface, and presents Plex with an empty but healthy NFS library. A user pastes a TorBox token, chooses one completed MP4 or MKV from their account, and BlackPearl activates that logical file without downloading it in full or restarting the Compose stack.

The existing persistent-cache POC, HTTP rolling-cache POC, range-oriented `ReadAt` contract, and current FUSE implementation remain intact.

## User Journey

1. Run `docker compose -f compose.torbox.yaml up -d --build`.
2. Open `http://localhost:8082`.
3. Paste a TorBox API token and select **Find my videos**.
4. Choose one eligible MP4 or MKV. The title and optional year are prefilled from its filename and may be edited.
5. Select **Use with Plex**. BlackPearl validates the file, persists the token with mode `0600`, activates the range-backed catalog entry, and reloads PearlNFS.
6. Open Plex at `http://localhost:32402`, add or scan `/blackpearl/Movies`, and Direct Play the item.

The setup page remains available. **Change video** uses the stored token; **Replace token** accepts a new token. Applying a replacement is transactional: the currently active media remains available unless the new selection has been validated and persisted successfully.

## Runtime Architecture

The BlackPearl process owns four collaborating units:

- A control-plane HTTP server serves diagnostics, a static Next.js application, and same-origin setup APIs.
- A setup service validates credentials, discovers eligible account files, builds a candidate rolling runtime, persists configuration, and atomically activates it.
- A catalog switch implements the existing catalog interface. Before setup it lists no media. After activation it delegates reads and readiness to the active range-backed catalog.
- PearlNFS consumes the switch. Its reload operation builds a complete new namespace snapshot off-lock and swaps it under a mutex. Existing open handles keep their original source while new lookups see the selected file.

The data plane remains unchanged:

```text
Plex file read at offset N
  -> PearlNFS
  -> active Catalog
  -> Rolling cache
  -> TorBox Gateway
  -> strict HTTP byte range
```

No component assumes that the complete media file exists locally. The configured logical size comes from TorBox metadata. The rolling cache stores only bounded chunks and may evict them independently.

## Layer Boundaries

- `internal/handler/setup`: HTTP parsing, security middleware, status mapping, and static UI delivery. It calls only the setup service.
- `internal/service/setup`: discovery and apply orchestration. It depends on repository, gateway factory, runtime factory, and catalog reloader interfaces.
- `internal/repository/setup`: atomic storage of the token and non-secret selection metadata.
- `internal/gateway/torbox`: TorBox API mapping and normalization, including media discovery.
- `internal/domain`: setup configuration and media candidate value objects with no internal-package dependencies.
- `internal/core`: switchable catalog and generic remote-video registration.

The initial implementation keeps the existing `net/http` diagnostics surface to avoid an unrelated server migration. Its new API schema is documented in `api/openapi.yaml`, handlers remain thin, and the service/repository/gateway boundaries match the repository's architecture. Moving the whole control plane to Echo/oapi-codegen is a follow-up after the browser-first flow is proven; it is not allowed to delay the Plex/TorBox POC.

## Setup API

All responses use JSON, `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and a restrictive Content Security Policy. Mutation requests must use a loopback `Host`, a matching loopback `Origin`, and an `X-BlackPearl-CSRF` value obtained from `GET /api/setup/status`.

- `GET /api/setup/status`
  - Returns `setupRequired`, `tokenConfigured`, a per-process CSRF value, and selected media metadata.
  - Never returns the token, token path, provider URL, file hash, or signed URL.
- `POST /api/setup/discover`
  - Body: `{ "token": "..." }`; token may be omitted to reuse the saved token.
  - Returns eligible candidates with opaque object ID, display name, extension, and logical size.
  - Body is limited to 8 KiB and is never logged or traced.
- `PUT /api/setup/configuration`
  - Body: `{ "token": "...", "objectId": "17:3", "title": "Movie", "year": 2026 }`; token may be omitted when one is already saved.
  - Revalidates the candidate, builds the runtime, persists secure settings, activates it, reloads NFS, and returns the selected public metadata.

Errors return stable public codes and plain messages. Raw TorBox response bodies, tokens, signed download URLs, and internal paths are never returned.

## TorBox Discovery

The TorBox gateway reads the authorized account's torrent list with Bearer authentication. It accepts only torrents whose download is finished and present. It accepts only files that:

- have a positive logical size and stable hash;
- are not zipped or marked infected;
- have a case-insensitive `.mp4` or `.mkv` extension;
- are not obvious sample files; and
- have canonical positive torrent and file IDs.

Candidates are sorted case-insensitively by display name. Discovery does not request a download URL or media bytes.

## Secure Persistence

Setup data lives beneath `/var/lib/blackpearl/setup`, which is part of the existing named data volume:

- directory mode `0700`;
- `torbox.token` mode `0600`, containing exactly one token without surrounding whitespace;
- `configuration.json` mode `0600`, containing only selection metadata.

Writes use a same-directory temporary file, explicit mode, file sync, rename, and directory sync. No secret is stored in the repository, browser storage, Compose environment, logs, traces, SQLite, or API responses. The React component retains a newly typed token only in memory and clears it after a successful apply.

## Startup and Reload Semantics

The TorBox Compose profile sets rolling mode and provider defaults but does not require a token or object ID. In setup mode, configuration validation permits these fields to be absent as a pair. BlackPearl starts the database, rolling cache, catalog switch, NFS listener, and HTTP server. `/healthz` returns 200 once those process-level dependencies are live. `/readyz` returns 503 with `setup_required` until media is active.

Compose uses `/healthz` for the BlackPearl healthcheck, allowing Plex to mount the empty NFS export. On startup with saved configuration, BlackPearl validates and activates the selection before reporting ready. If saved credentials are no longer usable, it stays live in setup-required state and does not expose stale media.

Applying a configuration follows prepare-commit-activate ordering:

1. validate token and selected metadata;
2. construct and probe the range provider and rolling source;
3. register the logical file in the catalog;
4. write token and configuration atomically;
5. activate the new catalog and reload NFS;
6. close superseded runtime resources after the swap.

If any step before activation fails, the old runtime remains active. If NFS reload fails, activation is rolled back and the API reports failure.

## Web Interface

The UI is a small static Next.js application embedded into the Go binary. It uses React 19, TypeScript strict mode, Bun, Vitest, and Testing Library. The visual direction is a restrained ship-manifest control panel: ink-black background, warm paper surfaces, brass accent, crisp borders, compact typography, and clear operational state. It avoids gradients, glass effects, oversized hero copy, excessive rounded cards, and generic dashboard decoration.

The interface has explicit states for first setup, validating, choosing media, applying, ready, authentication failure, provider failure, and no eligible media. File sizes are human-readable, keyboard navigation is complete, focus states are visible, and status changes use an ARIA live region.

## Compose and Portability

`compose.torbox.yaml` remains a standard Docker Compose stack using the existing NFS named-volume bridge. It publishes BlackPearl setup/diagnostics only as `127.0.0.1:8082` and Plex only as `127.0.0.1:32402`. The setup volume is the existing `blackpearl-data` named volume. No Linux host bind propagation, FUSE device, privileged mode, custom Plex client, or host command containing the API token is required.

This target applies equally to Docker Desktop on macOS and Windows and native Linux Docker. The existing NFS experiment already established the portable sibling-container mount approach; the new acceptance test must confirm the configured logical file is visible through the volume and supports non-sequential reads.

## Acceptance Criteria

- The TorBox stack starts with no token or object ID and both BlackPearl `/healthz` and Plex health succeed.
- `http://localhost:8082` renders the setup UI and is not published on a non-loopback host address.
- A valid token discovers only eligible completed MP4/MKV files without downloading media content.
- Applying one selection persists the token and configuration securely, activates the same range-oriented rolling path used by the existing provider, and reloads the NFS namespace without restarting the containers.
- Plex sees a normal seekable file with the correct `.mp4` or `.mkv` extension and TorBox logical size.
- A first read near the end of the file succeeds before the complete object has ever existed in the cache.
- Rolling cache usage remains at or below `BLACKPEARL_CACHE_MAX_BYTES`.
- Setup survives a BlackPearl container restart and never returns or logs the token.
- Replacing a bad token or invalid selection leaves the previously active file usable.
- Existing persistent, HTTP rolling, portable NFS, and TorBox range tests continue to pass.

## Explicit Non-Goals

- Multiple simultaneous library items, TV-season hierarchy, automatic resolver/search, Prowlarr, next-episode prefetch, subtitles, and eviction-policy redesign.
- Uploading torrents, altering TorBox account state, or acquiring unauthorized content.
- Refactoring the current FUSE implementation.
- Plex account automation or changing production Plex libraries.
