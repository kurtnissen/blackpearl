# TV episode hierarchy design

## Outcome

Authorized provider files can be published explicitly as movies or TV episodes. Plex receives canonical, seekable paths through the same `ReadAt` source and rolling cache:

- `Movies/Title (Year)/Title (Year).ext`
- `TV Shows/Show (Year)/Season 01/Show (Year) - S01E02 - Episode Title.ext`

## Model

The persisted setup item carries a `mediaType` discriminator. Movies use `title` and `year`. Episodes use `showTitle`, `year`, `season`, `episode`, and `title` as the episode title. Legacy items without `mediaType` normalize to `movie`.

The setup UI may suggest episode metadata from a conventional `SxxEyy` filename, but the user-visible fields remain editable and the domain validates them. No hidden metadata lookup or filename-only decision is authoritative.

## Filesystems and Plex

PearlFS and PearlNFS validate both canonical roots and materialize arbitrary directory depth for those two schemas. The Docker Plex container remains unmodified. The existing Movies library keeps `/blackpearl/Movies`; a separate TV Shows library uses `/blackpearl/TV Shows`.

## Acceptance

- Old movie-only manifests restore unchanged.
- Mixed movie/episode manifests persist and publish atomically.
- FUSE and NFS expose canonical TV hierarchy with logical sizes and random reads.
- Plex scans the episode in a TV library while the known H.264/AAC movie continues Direct Play.
- BlackPearl-only restart restores both roots through the existing Plex mount.
