package domain_test

import (
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestEpisodePlaybackQualifiesOnlyAfterBothProgressThresholds(t *testing.T) {
	t.Parallel()
	playback, err := domain.NewEpisodePlayback(
		"plex://show/5d9c086ce98e47001eb0f520",
		"TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4",
		1, 1, 130*time.Second, 20*time.Minute, domain.PlaybackStatePaused,
	)
	require.NoError(t, err)

	require.True(t, playback.Qualifies(120*time.Second, 10))
	require.False(t, playback.Qualifies(180*time.Second, 10))
	require.False(t, playback.Qualifies(120*time.Second, 11))
	require.False(t, playback.Qualifies(120*time.Second, 0))
	require.False(t, playback.Qualifies(120*time.Second, 100))
}

func TestNewEpisodePlaybackValidatesProviderNeutralEvidence(t *testing.T) {
	t.Parallel()
	validPath := "TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4"
	for _, test := range []struct {
		name       string
		externalID string
		path       string
		season     int
		episode    int
		offset     time.Duration
		duration   time.Duration
		state      domain.PlaybackState
	}{
		{name: "missing external ID", path: validPath, season: 1, episode: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "absolute path", externalID: "plex://show/id", path: "/blackpearl/" + validPath, season: 1, episode: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "movie path", externalID: "plex://show/id", path: "Movies/Film (2026)/Film (2026).mp4", season: 1, episode: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "unsafe path", externalID: "plex://show/id", path: "TV Shows/../secret.mp4", season: 1, episode: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "invalid season", externalID: "plex://show/id", path: validPath, episode: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "invalid episode", externalID: "plex://show/id", path: validPath, season: 1, offset: time.Minute, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "zero offset", externalID: "plex://show/id", path: validPath, season: 1, episode: 1, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "offset exceeds duration", externalID: "plex://show/id", path: validPath, season: 1, episode: 1, offset: 2 * time.Hour, duration: time.Hour, state: domain.PlaybackStatePlaying},
		{name: "overlong duration", externalID: "plex://show/id", path: validPath, season: 1, episode: 1, offset: time.Hour, duration: 8 * 24 * time.Hour, state: domain.PlaybackStatePlaying},
		{name: "unsupported state", externalID: "plex://show/id", path: validPath, season: 1, episode: 1, offset: time.Minute, duration: time.Hour, state: "buffering"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.NewEpisodePlayback(
				test.externalID, test.path, test.season, test.episode,
				test.offset, test.duration, test.state,
			)
			require.Error(t, err)
		})
	}
}

func TestEpisodeCoordinateOrdersAcrossSeasons(t *testing.T) {
	t.Parallel()
	current, err := domain.NewEpisodeCoordinate(1, 8)
	require.NoError(t, err)
	nextSeason, err := domain.NewEpisodeCoordinate(2, 1)
	require.NoError(t, err)

	require.True(t, nextSeason.After(current))
	require.False(t, current.After(nextSeason))
	require.Equal(t, 1, current.Season())
	require.Equal(t, 8, current.Episode())

	_, err = domain.NewEpisodeCoordinate(0, 1)
	require.Error(t, err)
	_, err = domain.NewEpisodeCoordinate(1, 1000)
	require.Error(t, err)
}
