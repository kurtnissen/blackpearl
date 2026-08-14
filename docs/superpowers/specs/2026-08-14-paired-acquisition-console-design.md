# Paired Acquisition Console Design

## Goal

Expose the tested Prowlarr-to-cached-TorBox acquisition path through the same
localhost-only, paired browser console that already controls the working Plex
manifest. A non-technical user should be able to connect Prowlarr once, search
for a movie or episode, and add an instant result to Plex without handling
commands or seeing provider secrets again.

## User flow

1. Complete the existing TorBox setup so BlackPearl has a paired browser
   session and a working Plex manifest.
2. In the ready screen, open **Find something new**.
3. If Prowlarr is not configured, enter its base URL and API key, reveal the key
   if needed, and select **Connect Prowlarr**.
4. Enter movie or TV details and select **Find and add to Plex**.
5. BlackPearl searches configured indexers, ranks results, permits only a
   TorBox-cached torrent, creates one cached-only account object, waits briefly
   for its files, and atomically publishes the selected video.
6. The console confirms the new Plex manifest and offers **Open Plex**.

No release URLs, magnets, indexer credentials, or TorBox credentials are
returned to the browser.

## Security boundary

- Acquisition mutations reuse the setup handler's exact Host, Origin, CSRF,
  bootstrap, and setup-session checks.
- Prowlarr settings can be saved only after the browser proves access to the
  already configured TorBox setup session.
- Status exposes only `configured: true|false`; it never returns the Prowlarr
  endpoint or API key.
- The Prowlarr API key is stored in a separate private, atomically replaced file
  under the existing Docker setup volume. This avoids weakening the existing
  TorBox token/manifest generation transaction.
- Provider errors are normalized at service boundaries. Handler logs and API
  responses never contain keys, magnets, signed URLs, response bodies, or
  private file paths.
- The endpoint does not accept arbitrary release locators. Search intent is
  validated server-side, and the backend chooses only ranked cached results.

## Architecture

### Domain

A generic immutable search-provider setting contains provider name, absolute
HTTP(S) endpoint, and write-only credential. The current provider must be
`prowlarr`, but persistence does not bake Prowlarr fields into setup state.

### Repository

A dedicated acquisition-settings repository provides context-aware `Load` and
`Save`. It enforces a private directory, bounded JSON, `0600` file mode,
temporary-file sync, atomic rename, and directory sync. Existing production
media and setup generations are untouched.

### Gateways and services

Prowlarr gains a read-only readiness probe. An acquisition coordinator owns:

- validating and probing new search-provider settings before saving them;
- loading saved settings and the saved TorBox token;
- building one isolated Prowlarr search gateway and one TorBox gateway per
  acquisition;
- composing the ranked resolver with the cached acquisition service;
- publishing through the setup service's atomic acquired-media boundary.

### HTTP

The existing setup handler gains optional acquisition routes so every route
shares one CSRF value and pairing implementation:

- `GET /api/acquisition/status` returns only configured state;
- `PUT /api/acquisition/settings` validates, probes, and saves Prowlarr settings;
- `POST /api/acquisition/acquire` accepts movie/episode intent and returns the
  updated public Plex manifest.

Existing setup routes and clients continue to work when acquisition is not
wired.

## Failure behavior

- Invalid endpoint/key shape: `422 invalid_settings`.
- Prowlarr rejects the key: `401 search_unauthorized`.
- No instant TorBox result: `404 not_cached` with a clear try-another-title
  message and no TorBox mutation.
- Cached object has no matching video: `422 no_playable_media`.
- Provider/runtime failure: `503 acquisition_unavailable`.
- Unpaired browser: existing setup pairing response.

## Acceptance

- Repository mode, durability, cancellation, bounds, and secret non-echo tests.
- Prowlarr readiness TLS contract tests.
- Coordinator tests for configure/save, saved credential reuse, resolver/gateway
  wiring, and sanitized failures.
- Handler contract tests for every route and security/error mapping.
- Vitest user-flow coverage for settings, movie and episode inputs, success,
  no-cache, authorization, and responsive rendering.
- Full race, coverage, Compose, lint, test, and production-build checks.
- Live browser evidence remains explicitly pending until a real authorized
  Prowlarr instance is configured.
