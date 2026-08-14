# Multi-item TorBox library implementation plan

1. Add `SetupManifest` validation and deterministic remote-media IDs with failing domain/core tests first.
2. Migrate setup persistence to `manifest.json` with legacy `configuration.json` loading tests.
3. Refactor setup service/runtime factory to validate, prepare, persist, restore, and roll back complete manifests atomically.
4. Extend the OpenAPI contract and handler while accepting the legacy one-item request.
5. Replace the single radio picker with searchable bounded multi-select and selected-item metadata editing.
6. Run static analysis, race/coverage tests, frontend checks, and all Compose contract tests.
7. Rebuild the isolated macOS stack; verify multi-file NFS visibility, Plex scan, Direct Play, seeking, cache bounds, and restart recovery in Brave.
