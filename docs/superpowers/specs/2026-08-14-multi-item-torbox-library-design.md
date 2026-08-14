# Multi-item TorBox library design

## Outcome

BlackPearl publishes a bounded manifest of authorized TorBox videos as ordinary seekable Plex files. The complete bytes for any item may remain remote; every file continues to use the provider-neutral `ReadAt` path and the manifest shares one process-lifetime rolling-cache quota.

## Boundaries

- One saved TorBox credential authorizes the manifest.
- A manifest contains 1-100 movie selections for this slice.
- Object IDs and Plex virtual paths must be unique.
- Runtime preparation validates every selected remote object's current size before publishing anything.
- Persistence and NFS publication remain transactional: failure preserves the prior manifest and issued NFS handles.
- Existing single-selection `configuration.json` generations load as a one-item manifest. New saves use `manifest.json`.
- The setup API remains localhost paired, write-only for credentials, and bounded to prevent scanner or request amplification.
- The UI searches the discovered account locally, renders at most 100 matching rows, and lets the user select multiple items. It does not expose every account file to Plex automatically.

## Domain and API

`SetupManifest` owns validated `[]SetupConfiguration`. The public setup status exposes `selectedItems`; the legacy `selected` field remains during migration and points at the first item. `PUT /api/setup/configuration` accepts `items`, with the old single-item shape retained for compatibility.

Each item keeps the current movie metadata shape. Media IDs become a deterministic digest of provider and object ID so one catalog can contain many entries. Movie paths remain:

`Movies/<Title> (<Year>)/<Title> (<Year>).<ext>`

Episode hierarchy is deliberately not inferred from filenames in this slice. The manifest type is the seam for a later explicit movie/episode discriminator.

## Runtime transaction

1. Resolve and authorize the token.
2. Discover current eligible objects once.
3. Validate every requested item against discovery metadata.
4. Prepare a fresh catalog and validate remote size metadata for every item.
5. Probe the catalog.
6. Atomically save token plus manifest generation.
7. Atomically replace the NFS namespace/catalog snapshot.
8. Roll persistence back if publication fails.

The process-lifetime `RollingPool` remains outside runtime replacement so old and new NFS handles safely share quota and cached chunks.

## Acceptance

- Legacy one-item saved state restores without manual migration.
- Two or more selections appear simultaneously under the Plex Movies root.
- Random reads and concurrent reads work for every item.
- Reconfiguration and restart preserve the manifest.
- Direct Play and seeking of an H.264/AAC MP4 work while cache usage stays below the logical library size.
- All Go race/coverage checks, frontend tests/build, Compose portability checks, and live Plex acceptance pass.
