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

func TestNewSetupEpisodeConfigurationPreservesCanonicalTVMetadata(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "Shows/Example.S01E02.mkv", 2048)
	require.NoError(t, err)

	configuration, err := domain.NewSetupEpisodeConfiguration(candidate, "Example Show", 2024, 1, 2, "The Second")

	require.NoError(t, err)
	require.Equal(t, domain.MediaTypeEpisode, configuration.MediaType)
	require.Equal(t, "Example Show", configuration.ShowTitle)
	require.Equal(t, "The Second", configuration.Title)
	require.Equal(t, 1, configuration.Season)
	require.Equal(t, 2, configuration.Episode)
}

func TestNewSetupManifestNormalizesLegacyMovieAndValidatesEpisode(t *testing.T) {
	t.Parallel()
	movieCandidate, err := domain.NewMediaCandidate("17:3", "Movie.mp4", 1024)
	require.NoError(t, err)
	legacyMovie := domain.SetupConfiguration{
		ObjectID: movieCandidate.ObjectID, Name: movieCandidate.Name, Extension: movieCandidate.Extension,
		Size: movieCandidate.Size, Title: "Movie", Year: 2024,
	}
	episodeCandidate, err := domain.NewMediaCandidate("17:4", "Show.S01E01.mkv", 2048)
	require.NoError(t, err)
	episode, err := domain.NewSetupEpisodeConfiguration(episodeCandidate, "Example Show", 2024, 1, 1, "Pilot")
	require.NoError(t, err)

	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{legacyMovie, episode})

	require.NoError(t, err)
	require.Equal(t, domain.MediaTypeMovie, manifest.Items[0].MediaType)
	require.Equal(t, domain.MediaTypeEpisode, manifest.Items[1].MediaType)
}

func TestNewSetupManifestValidatesMultipleUniqueMovies(t *testing.T) {
	t.Parallel()
	firstCandidate, err := domain.NewMediaCandidate("17:3", "Films/First.mp4", 1024)
	require.NoError(t, err)
	secondCandidate, err := domain.NewMediaCandidate("17:4", "Films/Second.mkv", 2048)
	require.NoError(t, err)
	first, err := domain.NewSetupConfiguration(firstCandidate, "First", 2024)
	require.NoError(t, err)
	second, err := domain.NewSetupConfiguration(secondCandidate, "Second", 2025)
	require.NoError(t, err)

	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{first, second})

	require.NoError(t, err)
	require.Equal(t, []domain.SetupConfiguration{first, second}, manifest.Items)
	manifest.Items[0].Title = "changed"
	require.Equal(t, "First", first.Title)
}

func TestNewSetupManifestRejectsEmptyOversizedAndDuplicateSelections(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "Films/Example.mp4", 1024)
	require.NoError(t, err)
	configuration, err := domain.NewSetupConfiguration(candidate, "Example", 2024)
	require.NoError(t, err)

	tests := []struct {
		name  string
		items []domain.SetupConfiguration
		want  string
	}{
		{name: "empty", want: "at least one"},
		{name: "too many", items: make([]domain.SetupConfiguration, domain.MaximumSetupManifestItems+1), want: "at most"},
		{name: "duplicate object", items: []domain.SetupConfiguration{configuration, configuration}, want: "duplicate object"},
		{name: "duplicate path", items: []domain.SetupConfiguration{configuration, withObjectID(t, configuration, "17:4")}, want: "duplicate Plex path"},
	}
	for index := range tests {
		test := tests[index]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.name == "too many" {
				for itemIndex := range test.items {
					test.items[itemIndex] = configuration
				}
			}
			_, manifestErr := domain.NewSetupManifest(test.items)
			require.ErrorContains(t, manifestErr, test.want)
		})
	}
}

func withObjectID(t *testing.T, configuration domain.SetupConfiguration, objectID string) domain.SetupConfiguration {
	t.Helper()
	candidate, err := domain.NewMediaCandidate(objectID, configuration.Name, configuration.Size)
	require.NoError(t, err)
	result, err := domain.NewSetupConfiguration(candidate, configuration.Title, configuration.Year)
	require.NoError(t, err)
	return result
}
