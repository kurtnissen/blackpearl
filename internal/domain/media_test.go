package domain_test

import (
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewMovieBuildsCanonicalPlexPath(t *testing.T) {
	t.Parallel()

	media, err := domain.NewMovie("poc", "BlackPearl POC", 2026, ".mp4", 42, "abc")

	require.NoError(t, err)
	require.Equal(t, "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4", media.VirtualPath)
	require.Equal(t, int64(42), media.Size)
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

			_, err := domain.NewMovie("id", test.title, 2026, test.extension, 1, "key")

			require.Error(t, err)
		})
	}
}

func TestNewMovieRejectsInvalidRequiredValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       domain.MediaID
		title    string
		year     int
		size     int64
		cacheKey string
	}{
		{name: "empty id", title: "Movie", year: 2026, size: 1, cacheKey: "key"},
		{name: "empty title", id: "id", year: 2026, size: 1, cacheKey: "key"},
		{name: "invalid year", id: "id", title: "Movie", size: 1, cacheKey: "key"},
		{name: "negative size", id: "id", title: "Movie", year: 2026, size: -1, cacheKey: "key"},
		{name: "empty cache key", id: "id", title: "Movie", year: 2026, size: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewMovie(test.id, test.title, test.year, ".mp4", test.size, test.cacheKey)

			require.Error(t, err)
		})
	}
}
