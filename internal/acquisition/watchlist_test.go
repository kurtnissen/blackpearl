package acquisition_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/stretchr/testify/require"
)

func TestNewWatchlistItemNormalizesMovieIntent(t *testing.T) {
	t.Parallel()

	item, err := acquisition.NewWatchlistItem(acquisition.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: "plex://movie/example", MediaType: acquisition.WatchlistMediaTypeMovie,
		Title: "  Example Movie  ", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, "plex-watchlist", item.Source())
	require.Equal(t, "plex://movie/example", item.ExternalID())
	require.Equal(t, acquisition.WatchlistMediaTypeMovie, item.MediaType())
	require.Equal(t, "Example Movie", item.Title())
	require.Equal(t, 2026, item.Year())
	request, err := item.SearchRequest()
	require.NoError(t, err)
	require.Equal(t, "Example Movie 2026", request.Query())
}

func TestWatchlistShowRemainsObservableWithoutInventingEpisodeIntent(t *testing.T) {
	t.Parallel()

	item, err := acquisition.NewWatchlistItem(acquisition.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: "plex://show/example", MediaType: acquisition.WatchlistMediaTypeShow,
		Title: "Example Show", Year: 2020,
	})

	require.NoError(t, err)
	_, err = item.SearchRequest()
	require.ErrorIs(t, err, acquisition.ErrUnsupportedWatchlistMedia)
}

func TestWatchlistPolicyAndObservationRequireExplicitShowIntent(t *testing.T) {
	t.Parallel()
	movie := mustWatchlistItem(t, "plex://movie/policy", acquisition.WatchlistMediaTypeMovie)
	show := mustWatchlistItem(t, "plex://show/policy", acquisition.WatchlistMediaTypeShow)

	policy, err := acquisition.NewWatchlistPolicy(true, acquisition.WatchlistShowPolicyPilot)
	require.NoError(t, err)
	require.True(t, policy.AcquisitionEnabled())
	require.Equal(t, acquisition.WatchlistShowPolicyPilot, policy.ShowPolicy())
	_, err = acquisition.NewWatchlistPolicy(true, "season")
	require.Error(t, err)

	movieObservation, err := acquisition.NewWatchlistObservation(movie, true, 0, 0)
	require.NoError(t, err)
	require.True(t, movieObservation.AutoEligible())
	require.Equal(t, movie, movieObservation.Item())
	require.Zero(t, movieObservation.Season())
	require.Zero(t, movieObservation.Episode())

	pilot, err := acquisition.NewWatchlistObservation(show, true, 1, 1)
	require.NoError(t, err)
	require.True(t, pilot.AutoEligible())
	require.Equal(t, 1, pilot.Season())
	require.Equal(t, 1, pilot.Episode())

	observedOnly, err := acquisition.NewWatchlistObservation(show, false, 0, 0)
	require.NoError(t, err)
	require.False(t, observedOnly.AutoEligible())

	for _, test := range []struct {
		name     string
		item     acquisition.WatchlistItem
		eligible bool
		season   int
		episode  int
	}{
		{name: "movie coordinates", item: movie, eligible: true, season: 1, episode: 1},
		{name: "eligible show without coordinates", item: show, eligible: true},
		{name: "observation-only show coordinates", item: show, season: 1, episode: 1},
		{name: "show season too large", item: show, eligible: true, season: 100, episode: 1},
		{name: "show episode too large", item: show, eligible: true, season: 1, episode: 1000},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, observationErr := acquisition.NewWatchlistObservation(
				test.item, test.eligible, test.season, test.episode,
			)
			require.Error(t, observationErr)
		})
	}
}

func TestNewWatchlistItemRejectsInvalidProviderData(t *testing.T) {
	t.Parallel()

	valid := acquisition.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: "plex://movie/example", MediaType: acquisition.WatchlistMediaTypeMovie,
		Title: "Example", Year: 2026,
	}
	tests := []struct {
		name   string
		mutate func(*acquisition.WatchlistItemInput)
	}{
		{name: "invalid source", mutate: func(input *acquisition.WatchlistItemInput) { input.Source = "Plex" }},
		{name: "blank external ID", mutate: func(input *acquisition.WatchlistItemInput) { input.ExternalID = " " }},
		{name: "oversize external ID", mutate: func(input *acquisition.WatchlistItemInput) { input.ExternalID = strings.Repeat("x", 513) }},
		{name: "control external ID", mutate: func(input *acquisition.WatchlistItemInput) { input.ExternalID = "plex://movie/bad\n" }},
		{name: "unsupported type", mutate: func(input *acquisition.WatchlistItemInput) { input.MediaType = "season" }},
		{name: "blank title", mutate: func(input *acquisition.WatchlistItemInput) { input.Title = " " }},
		{name: "oversize title bytes", mutate: func(input *acquisition.WatchlistItemInput) { input.Title = strings.Repeat("é", 101) }},
		{name: "year too early", mutate: func(input *acquisition.WatchlistItemInput) { input.Year = 1887 }},
		{name: "year too late", mutate: func(input *acquisition.WatchlistItemInput) { input.Year = 2101 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := valid
			test.mutate(&input)
			_, err := acquisition.NewWatchlistItem(input)
			require.Error(t, err)
			require.False(t, errors.Is(err, acquisition.ErrUnsupportedWatchlistMedia))
		})
	}
}
