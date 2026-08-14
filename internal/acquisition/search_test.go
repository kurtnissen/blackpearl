package acquisition_test

import (
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewMovieSearchNormalizesValidIntent(t *testing.T) {
	t.Parallel()

	request, err := acquisition.NewMovieSearch("  Otherhood  ", 2019)

	require.NoError(t, err)
	require.Equal(t, domain.MediaTypeMovie, request.MediaType())
	require.Equal(t, "Otherhood", request.Title())
	require.Equal(t, 2019, request.Year())
	require.Zero(t, request.Season())
	require.Zero(t, request.Episode())
	require.Equal(t, "Otherhood 2019", request.Query())
}

func TestNewEpisodeSearchNormalizesValidIntent(t *testing.T) {
	t.Parallel()

	request, err := acquisition.NewEpisodeSearch("  Friends  ", 1994, 7, 2)

	require.NoError(t, err)
	require.Equal(t, domain.MediaTypeEpisode, request.MediaType())
	require.Equal(t, "Friends", request.Title())
	require.Equal(t, 1994, request.Year())
	require.Equal(t, 7, request.Season())
	require.Equal(t, 2, request.Episode())
	require.Equal(t, "Friends S07E02", request.Query())
}

func TestNewSearchRejectsInvalidIntent(t *testing.T) {
	t.Parallel()
	validTwoHundredBytes := strings.Repeat("é", 100)
	tests := []struct {
		name string
		call func() error
	}{
		{name: "blank movie title", call: func() error { _, err := acquisition.NewMovieSearch("  ", 2026); return err }},
		{name: "movie control character", call: func() error { _, err := acquisition.NewMovieSearch("Bad\nTitle", 2026); return err }},
		{name: "movie title over bytes", call: func() error { _, err := acquisition.NewMovieSearch(validTwoHundredBytes+"x", 2026); return err }},
		{name: "movie before year range", call: func() error { _, err := acquisition.NewMovieSearch("Movie", 1887); return err }},
		{name: "movie after year range", call: func() error { _, err := acquisition.NewMovieSearch("Movie", 2101); return err }},
		{name: "episode season negative", call: func() error { _, err := acquisition.NewEpisodeSearch("Show", 2026, -1, 1); return err }},
		{name: "episode season excessive", call: func() error { _, err := acquisition.NewEpisodeSearch("Show", 2026, 100, 1); return err }},
		{name: "episode number zero", call: func() error { _, err := acquisition.NewEpisodeSearch("Show", 2026, 1, 0); return err }},
		{name: "episode number excessive", call: func() error { _, err := acquisition.NewEpisodeSearch("Show", 2026, 1, 1000); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, test.call())
		})
	}

	_, err := acquisition.NewMovieSearch(validTwoHundredBytes, 2026)
	require.NoError(t, err)
}

func TestNewReleaseNormalizesValidTorrentByHash(t *testing.T) {
	t.Parallel()
	seeders := 42

	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		SourceID: "source-1", Title: "Otherhood.2019.1080p", Protocol: acquisition.ReleaseProtocolTorrent,
		Size: 10_000, Indexer: "authorized-indexer", InfoHash: "ABCDEF0123456789ABCDEF0123456789ABCDEF01", Seeders: &seeders,
	})

	require.NoError(t, err)
	require.Equal(t, "source-1", release.SourceID())
	require.Equal(t, "Otherhood.2019.1080p", release.Title())
	require.Equal(t, acquisition.ReleaseProtocolTorrent, release.Protocol())
	require.Equal(t, int64(10_000), release.Size())
	require.Equal(t, "authorized-indexer", release.Indexer())
	require.Equal(t, "abcdef0123456789abcdef0123456789abcdef01", release.InfoHash())
	require.Empty(t, release.MagnetURL())
	require.Empty(t, release.DownloadURL())
	require.Equal(t, 42, release.Seeders())
	require.True(t, release.HasSeeders())
}

func TestNewReleaseAcceptsValidProtocolLocators(t *testing.T) {
	t.Parallel()
	tests := []acquisition.ReleaseInput{
		{SourceID: "magnet", Title: "Movie", Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "one", MagnetURL: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=Movie"},
		{SourceID: "torrent-file", Title: "Movie", Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "one", DownloadURL: "https://prowlarr.test/download?id=1&token=signed"},
		{SourceID: "nzb", Title: "Episode", Protocol: acquisition.ReleaseProtocolUsenet, Size: 1, Indexer: "two", DownloadURL: "https://prowlarr.test/download/episode.nzb?key=signed"},
	}
	for _, input := range tests {
		input := input
		t.Run(input.SourceID, func(t *testing.T) {
			t.Parallel()
			_, err := acquisition.NewRelease(input)
			require.NoError(t, err)
		})
	}
}

func TestNewReleaseRejectsUnsafeOrIncompleteResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*acquisition.ReleaseInput)
	}{
		{name: "blank source ID", mutate: func(input *acquisition.ReleaseInput) { input.SourceID = " " }},
		{name: "control in title", mutate: func(input *acquisition.ReleaseInput) { input.Title = "Bad\tTitle" }},
		{name: "oversize title", mutate: func(input *acquisition.ReleaseInput) { input.Title = strings.Repeat("x", 201) }},
		{name: "unsupported protocol", mutate: func(input *acquisition.ReleaseInput) { input.Protocol = "web" }},
		{name: "zero size", mutate: func(input *acquisition.ReleaseInput) { input.Size = 0 }},
		{name: "blank indexer", mutate: func(input *acquisition.ReleaseInput) { input.Indexer = "" }},
		{name: "invalid hash", mutate: func(input *acquisition.ReleaseInput) { input.InfoHash = "not-a-hash" }},
		{name: "unsafe magnet scheme", mutate: func(input *acquisition.ReleaseInput) {
			input.InfoHash = ""
			input.MagnetURL = "https://example.test/file"
		}},
		{name: "magnet missing btih", mutate: func(input *acquisition.ReleaseInput) { input.InfoHash = ""; input.MagnetURL = "magnet:?dn=Movie" }},
		{name: "credentialed download URL", mutate: func(input *acquisition.ReleaseInput) {
			input.InfoHash = ""
			input.DownloadURL = "https://user:secret@example.test/file"
		}},
		{name: "fragmented download URL", mutate: func(input *acquisition.ReleaseInput) {
			input.InfoHash = ""
			input.DownloadURL = "https://example.test/file#secret"
		}},
		{name: "torrent missing locator", mutate: func(input *acquisition.ReleaseInput) { input.InfoHash = "" }},
		{name: "usenet missing download URL", mutate: func(input *acquisition.ReleaseInput) {
			input.Protocol = acquisition.ReleaseProtocolUsenet
			input.InfoHash = ""
		}},
		{name: "negative seeders", mutate: func(input *acquisition.ReleaseInput) { negative := -1; input.Seeders = &negative }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := acquisition.ReleaseInput{
				SourceID: "source", Title: "Movie", Protocol: acquisition.ReleaseProtocolTorrent,
				Size: 1, Indexer: "indexer", InfoHash: "abcdef0123456789abcdef0123456789abcdef01",
			}
			test.mutate(&input)
			_, err := acquisition.NewRelease(input)
			require.Error(t, err)
		})
	}
}

func TestProviderCapabilitiesReturnsIndependentProtocolCopy(t *testing.T) {
	t.Parallel()
	capabilities := acquisition.NewProviderCapabilities(
		[]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent}, true, true, false,
	)

	protocols := capabilities.Protocols()
	protocols[0] = acquisition.ReleaseProtocolUsenet

	require.Equal(t, []acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent}, capabilities.Protocols())
	require.True(t, capabilities.InfoHashes())
	require.True(t, capabilities.MagnetURLs())
	require.False(t, capabilities.DownloadURLs())
}
