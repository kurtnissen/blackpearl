# Automatic Plex Library Refresh Design

## Goal

After BlackPearl successfully publishes a changed logical media manifest, the
isolated Plex server should scan the matching Movies and TV Shows libraries
without requiring a manual scan. This must remain best-effort: a stopped or
misconfigured Plex server cannot roll back, corrupt, or hide an already durable
BlackPearl manifest.

## Constraints

- BlackPearl and Plex remain on disjoint Docker networks.
- Plex credentials continue to come from the project-scoped read-only Plex
  configuration volume and are never persisted by BlackPearl or returned to the
  browser.
- Requests carry the Plex token only in `X-Plex-Token`; redirects are disabled.
- Section discovery and response bodies are bounded.
- Only Plex libraries rooted exactly at `/blackpearl/Movies` or
  `/blackpearl/TV Shows` are refreshed.
- Publication remains atomic and successful even if Plex is offline.
- The same binary supports an explicitly configured Plex HTTP endpoint on
  Docker Desktop and native Linux.

## Architecture

`setupPublisher` keeps its existing atomic order: replace the NFS namespace,
then activate the catalog switch. Only after both operations succeed does it
send a nonblocking notification to a process-lifetime refresh worker. The
notification has no error return and therefore cannot become part of the setup
transaction or trigger persistence rollback.

The worker debounces notifications, calls a narrow Plex library refresher, and
retries at a bounded interval while the process is alive. Multiple publications
coalesce into one pending scan. Errors are reported through an injected callback
owned by application wiring, where they are logged without private response
bodies.

The Plex library refresher loads the current token for each attempt, requests
`/library/sections` as JSON, finds section keys containing one of the two exact
BlackPearl roots, and requests `/library/sections/{key}/refresh` for each unique
match. It rejects redirects, oversized or malformed responses, invalid section
keys, authentication failures, and a response with no matching sections.

## Configuration and portability

Browser setup gains two settings:

- `BLACKPEARL_PLEX_REFRESH_ENABLED`, default `false`.
- `BLACKPEARL_PLEX_REFRESH_URL`, required when enabled and expressed as an
  absolute HTTP(S) URL without credentials, query, or fragment.

The TorBox Compose profile enables the feature with
`http://host.docker.internal:32402`. Docker Desktop provides that hostname;
Compose also declares the standard `host-gateway` mapping for native Linux.
Deployments whose Plex host port is not reachable at that address can override
the URL without changing BlackPearl. The setup UI does not expose the Plex token
or endpoint.

## Failure behavior

- A failed NFS/catalog publication emits no refresh notification.
- A failed refresh logs a sanitized warning and retries; the manifest stays
  active and persisted.
- A newer publication while retrying is coalesced and the eventual successful
  scan observes the newest namespace.
- Shutdown cancels an in-flight request and stops retries promptly.
- A token or section authorization error is treated as retryable because Plex
  account state can change without restarting BlackPearl.

## Acceptance

Automated tests prove exact token headers and paths, bounded JSON parsing,
section filtering, redirect/auth/error handling, notification coalescing,
retry, shutdown, and publication independence. Compose checks prove network
separation remains intact. Live macOS acceptance publishes or re-publishes a
manifest, observes Plex receive the refresh request, and confirms existing
Direct Play and seeking remain functional.

