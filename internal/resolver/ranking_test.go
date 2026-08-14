package resolver_test

import (
	"context"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/resolver"
	"github.com/stretchr/testify/require"
)

func TestSearchRanksCompleteMovieTitleMatchFirst(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Otherhood", 2019)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "partial", "Other Movie 2019", 100, "", 100),
		searchTorrent(t, "prowlarr", "match", "Otherhood.2019.1080p", 1000, "", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"match"}, releaseIDs(actual))
}

func TestSearchDropsPreviewBeforeCachedFirstPlanningCanPromoteIt(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Sintel", 2010)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "preview", "Preview: Sintel (2010) — Coming Next", 15_000_000, "abcdef0123456789abcdef0123456789abcdef01", 1),
		searchTorrent(t, "prowlarr", "movie", "Sintel (2010)", 139_000_000, "abcdef1123456789abcdef0123456789abcdef01", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"movie"}, releaseIDs(actual))
}

func TestSearchDropsAuxiliaryVideosAfterCompleteMovieTitleAndYear(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("House on Haunted Hill", 1959)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "trailer", "House on Haunted Hill (1959) - Trailer", 15_000_000, "abcdef0123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "teaser", "House on Haunted Hill (1959) - Teaser", 15_000_000, "abcdef1123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "sample", "House on Haunted Hill (1959) - Sample", 15_000_000, "abcdef2123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "preview", "House on Haunted Hill (1959) - Preview", 15_000_000, "abcdef3123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "featurette", "House on Haunted Hill (1959) - Featurette", 15_000_000, "abcdef4123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "movie", "House on Haunted Hill (1959) - Full-Length Horror Classic", 197_000_000, "abcdef5123456789abcdef0123456789abcdef01", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"movie"}, releaseIDs(actual))
}

func TestSearchKeepsTrailerKeywordWhenPartOfRequestedMovieTitle(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("The Trailer", 2026)
	require.NoError(t, err)
	movie := searchTorrent(t, "prowlarr", "movie", "The Trailer (2026) 1080p", 197_000_000, "abcdef0123456789abcdef0123456789abcdef01", 1)

	actual := searchWithReleases(t, request, []acquisition.Release{movie})

	require.Equal(t, []string{"movie"}, releaseIDs(actual))
}

func TestSearchDoesNotTreatPartialWordAsCompleteTitleMatch(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "partial-word", "BadMovie 2026", 100, "", 100),
		searchTorrent(t, "prowlarr", "complete-word", "Movie 2026", 1000, "", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"complete-word"}, releaseIDs(actual))
}

func TestSearchRanksCompleteEpisodeTokenFirst(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Friends", 1994, 7, 2)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "wrong-episode", "Friends.S07E01.1080p", 100, "", 100),
		searchTorrent(t, "prowlarr", "match", "Friends - S07E02 - Episode", 1000, "", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"match"}, releaseIDs(actual))
}

func TestSearchDropsAuxiliaryEpisodeAfterCompleteEpisodeToken(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Show", 2026, 1, 2)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "prowlarr", "trailer", "Show S01E02 Trailer", 15_000_000, "abcdef0123456789abcdef0123456789abcdef01", 100),
		searchTorrent(t, "prowlarr", "episode", "Show S01E02 Episode", 197_000_000, "abcdef1123456789abcdef0123456789abcdef01", 1),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"episode"}, releaseIDs(actual))
}

func TestSearchRanksHashThenSeedersThenSizeAndStableIdentity(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchTorrent(t, "zeta", "stable-z", "Movie 2026", 100, "abcdef0123456789abcdef0123456789abcdef01", 1),
		searchTorrent(t, "alpha", "stable-b", "Movie 2026", 100, "abcdef1123456789abcdef0123456789abcdef01", 10),
		searchTorrent(t, "alpha", "stable-a", "Movie 2026", 100, "abcdef2123456789abcdef0123456789abcdef01", 10),
		searchTorrent(t, "alpha", "smaller", "Movie 2026", 50, "abcdef3123456789abcdef0123456789abcdef01", 10),
		searchTorrent(t, "alpha", "magnet-only", "Movie 2026", 10, "", 1000),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"smaller", "stable-a", "stable-b", "stable-z", "magnet-only"}, releaseIDs(actual))
}

func TestSearchDeduplicatesByProtocolAndHashKeepingHighestRanked(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	sharedHash := "abcdef0123456789abcdef0123456789abcdef01"
	releases := []acquisition.Release{
		searchTorrent(t, "first", "low", "Movie 2026", 100, sharedHash, 1),
		searchTorrent(t, "second", "high", "Movie 2026", 100, sharedHash, 50),
		searchTorrent(t, "first", "unique", "Movie 2026", 100, "abcdef1123456789abcdef0123456789abcdef01", 10),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"high", "unique"}, releaseIDs(actual))
}

func TestSearchDeduplicatesHashlessReleaseWithinProviderOnly(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Show", 2026, 1, 2)
	require.NoError(t, err)
	releases := []acquisition.Release{
		searchUsenet(t, "first", "same", "Show S01E02 large", 200),
		searchUsenet(t, "first", "same", "Show S01E02 small", 100),
		searchUsenet(t, "second", "same", "Show S01E02 other", 150),
	}

	actual := searchWithReleases(t, request, releases)

	require.Equal(t, []string{"same:first", "same:second"}, releaseProviderIDs(actual))
	require.Equal(t, int64(100), actual[0].Size())
}

func TestSearchDropsZeroValueReleaseFromProvider(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	valid := searchTorrent(t, "prowlarr", "valid", "Movie 2026", 100, "", 1)

	actual := searchWithReleases(t, request, []acquisition.Release{{}, valid})

	require.Equal(t, []string{"valid"}, releaseIDs(actual))
}

func searchWithReleases(t *testing.T, request acquisition.SearchRequest, releases []acquisition.Release) []acquisition.Release {
	t.Helper()
	service := resolver.NewSearcher(&fakeSearchProvider{name: "fixture", releases: releases})
	actual, err := service.Search(context.Background(), request)
	require.NoError(t, err)
	return actual
}

func searchTorrent(t *testing.T, provider string, sourceID string, title string, size int64, hash string, seeders int) acquisition.Release {
	t.Helper()
	input := acquisition.ReleaseInput{
		Provider: provider, SourceID: sourceID, Title: title, Protocol: acquisition.ReleaseProtocolTorrent,
		Size: size, Indexer: "indexer", InfoHash: hash, Seeders: &seeders,
	}
	if hash == "" {
		input.MagnetURL = "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	}
	return mustSearchRelease(t, input)
}

func searchUsenet(t *testing.T, provider string, sourceID string, title string, size int64) acquisition.Release {
	t.Helper()
	return mustSearchRelease(t, acquisition.ReleaseInput{
		Provider: provider, SourceID: sourceID, Title: title, Protocol: acquisition.ReleaseProtocolUsenet,
		Size: size, Indexer: "indexer", DownloadURL: "https://prowlarr.test/download/fixture.nzb",
	})
}

func releaseIDs(releases []acquisition.Release) []string {
	result := make([]string, 0, len(releases))
	for _, release := range releases {
		result = append(result, release.SourceID())
	}
	return result
}

func releaseProviderIDs(releases []acquisition.Release) []string {
	result := make([]string, 0, len(releases))
	for _, release := range releases {
		result = append(result, release.SourceID()+":"+release.Provider())
	}
	return result
}
