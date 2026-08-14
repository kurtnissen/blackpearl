package domain_test

import (
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewMediaCandidateAcceptsCanonicalVideos(t *testing.T) {
	t.Parallel()

	candidate, err := domain.NewMediaCandidate("17:3", "Films/Example.MKV", 9_876)

	require.NoError(t, err)
	require.Equal(t, domain.MediaCandidate{
		ObjectID:  "17:3",
		Name:      "Films/Example.MKV",
		Extension: ".mkv",
		Size:      9_876,
	}, candidate)
}

func TestNewMediaCandidateRejectsUnsafeOrUnsupportedValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		objectID string
		path     string
		size     int64
	}{
		{name: "noncanonical object", objectID: "017:3", path: "movie.mkv", size: 1},
		{name: "unsupported extension", objectID: "17:3", path: "movie.avi", size: 1},
		{name: "unsafe name", objectID: "17:3", path: "../movie.mkv", size: 1},
		{name: "empty file", objectID: "17:3", path: "movie.mkv", size: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewMediaCandidate(test.objectID, test.path, test.size)

			require.Error(t, err)
		})
	}
}

func TestNewSetupConfigurationSanitizesFilenameTitle(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "Movies/The.Example.2024.mkv", 100)
	require.NoError(t, err)

	configuration, err := domain.NewSetupConfiguration(candidate, "  The Example  ", 2024)

	require.NoError(t, err)
	require.Equal(t, "The Example", configuration.Title)
	require.Equal(t, 2024, configuration.Year)
	require.Equal(t, candidate.ObjectID, configuration.ObjectID)
}

func TestNewSetupConfigurationRejectsUnsafeTitleAndYear(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "movie.mp4", 100)
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		title string
		year  int
	}{
		{name: "path title", title: "Bad/Title", year: 2026},
		{name: "year before cinema", title: "Movie", year: 1887},
		{name: "future bound", title: "Movie", year: 2101},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewSetupConfiguration(candidate, test.title, test.year)

			require.Error(t, err)
		})
	}
}

func TestNewSetupConfigurationEnforcesPlexTitleByteLimit(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "movie.mp4", 100)
	require.NoError(t, err)

	configuration, err := domain.NewSetupConfiguration(candidate, strings.Repeat("a", 200), 2026)
	require.NoError(t, err)
	require.Len(t, configuration.Title, 200)

	_, err = domain.NewSetupConfiguration(candidate, strings.Repeat("a", 201), 2026)
	require.ErrorContains(t, err, "200 bytes")
	_, err = domain.NewSetupConfiguration(candidate, strings.Repeat("é", 101), 2026)
	require.ErrorContains(t, err, "200 bytes")
}
