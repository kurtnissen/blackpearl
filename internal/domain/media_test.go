package domain_test

import (
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewMovieBuildsCanonicalPlexPath(t *testing.T) {
	t.Parallel()

	media, err := domain.NewMovie("poc", "BlackPearl POC", 2026, ".mp4", 42, validBacking())

	require.NoError(t, err)
	require.Equal(t, "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4", media.VirtualPath)
	require.Equal(t, int64(42), media.Size)
}

func TestNewEpisodeBuildsCanonicalPlexPath(t *testing.T) {
	t.Parallel()

	media, err := domain.NewEpisode("episode", "Example Show", 2024, 1, 2, "The Second", ".mkv", 84, validBacking())

	require.NoError(t, err)
	require.Equal(t, domain.MediaTypeEpisode, media.Type)
	require.Equal(t, "TV Shows/Example Show (2024)/Season 01/Example Show (2024) - S01E02 - The Second.mkv", media.VirtualPath)
	require.Equal(t, "The Second", media.Title)
	require.Equal(t, int64(84), media.Size)
}

func TestNewEpisodeRejectsUnsafeOrOutOfRangeMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		showTitle    string
		season       int
		episode      int
		episodeTitle string
	}{
		{name: "unsafe show", showTitle: "../Show", season: 1, episode: 1, episodeTitle: "Pilot"},
		{name: "negative season", showTitle: "Show", season: -1, episode: 1, episodeTitle: "Pilot"},
		{name: "large season", showTitle: "Show", season: 100, episode: 1, episodeTitle: "Pilot"},
		{name: "zero episode", showTitle: "Show", season: 1, episodeTitle: "Pilot"},
		{name: "large episode", showTitle: "Show", season: 1, episode: 1000, episodeTitle: "Pilot"},
		{name: "unsafe episode title", showTitle: "Show", season: 1, episode: 1, episodeTitle: "Bad/Title"},
	}
	for index := range tests {
		test := tests[index]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.NewEpisode("episode", test.showTitle, 2024, test.season, test.episode, test.episodeTitle, ".mkv", 84, validBacking())
			require.Error(t, err)
		})
	}
}

func TestNewMovieRejectsUnsafePathSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		title     string
		extension string
	}{
		{name: "parent traversal title", title: "../escape", extension: ".mp4"},
		{name: "path separator title", title: "bad/title", extension: ".mp4"},
		{name: "path separator extension", title: "Movie", extension: "/mp4"},
		{name: "missing extension dot", title: "Movie", extension: "mp4"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewMovie("id", test.title, 2026, test.extension, 1, validBacking())

			require.Error(t, err)
		})
	}
}

func TestNewMovieRejectsTitleBeyondPlexByteLimit(t *testing.T) {
	t.Parallel()

	_, err := domain.NewMovie("id", strings.Repeat("a", 201), 2026, ".mp4", 1, validBacking())

	require.ErrorContains(t, err, "200 bytes")
}

func TestNewMovieRejectsInvalidRequiredValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      domain.MediaID
		title   string
		year    int
		size    int64
		backing domain.BackingRef
	}{
		{name: "empty id", title: "Movie", year: 2026, size: 1, backing: validBacking()},
		{name: "empty title", id: "id", year: 2026, size: 1, backing: validBacking()},
		{name: "invalid year", id: "id", title: "Movie", size: 1, backing: validBacking()},
		{name: "negative size", id: "id", title: "Movie", year: 2026, size: -1, backing: validBacking()},
		{name: "empty backing", id: "id", title: "Movie", year: 2026, size: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewMovie(test.id, test.title, test.year, ".mp4", test.size, test.backing)

			require.Error(t, err)
		})
	}
}

func TestNewBackingRefRejectsUnsafeOrMissingParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		objectID string
	}{
		{provider: "", objectID: "object"},
		{provider: "pearlcache", objectID: ""},
		{provider: "../provider", objectID: "object"},
		{provider: "pearlcache", objectID: "\x00object"},
	}

	for _, test := range tests {
		_, err := domain.NewBackingRef(test.provider, test.objectID)
		require.Error(t, err)
	}
}

func validBacking() domain.BackingRef {
	backing, err := domain.NewBackingRef("pearlcache", "object")
	if err != nil {
		panic(err)
	}
	return backing
}
