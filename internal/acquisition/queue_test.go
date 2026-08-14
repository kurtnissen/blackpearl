package acquisition_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/stretchr/testify/require"
)

func TestNewWatchlistClaimRequiresValidatedIntentAndPositiveLeaseValues(t *testing.T) {
	t.Parallel()
	item := mustWatchlistItem(t, "plex://movie/example", acquisition.WatchlistMediaTypeMovie)

	claim, err := acquisition.NewWatchlistClaim(item, 2, 3)

	require.NoError(t, err)
	require.Equal(t, item, claim.Item())
	require.Equal(t, int64(2), claim.LeaseVersion())
	require.Equal(t, 3, claim.Attempt())

	for _, test := range []struct {
		name    string
		item    acquisition.WatchlistItem
		version int64
		attempt int
	}{
		{name: "zero item", version: 1, attempt: 1},
		{name: "zero version", item: item, attempt: 1},
		{name: "zero attempt", item: item, version: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, claimErr := acquisition.NewWatchlistClaim(test.item, test.version, test.attempt)
			require.Error(t, claimErr)
		})
	}
}

func TestNewWatchlistJobClaimValidatesDurableJobIdentity(t *testing.T) {
	t.Parallel()
	item := mustWatchlistItem(t, "plex://movie/background", acquisition.WatchlistMediaTypeMovie)
	jobID := "0123456789abcdef0123456789abcdef"

	claim, err := acquisition.NewWatchlistJobClaim(item, 2, 3, jobID)

	require.NoError(t, err)
	require.Equal(t, jobID, claim.BackgroundJobID())

	for _, invalid := range []string{
		"", "short", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcdeg",
	} {
		_, claimErr := acquisition.NewWatchlistJobClaim(item, 2, 3, invalid)
		require.Error(t, claimErr)
	}
}

func TestWatchlistIntentClaimOwnsExactEpisodeSearch(t *testing.T) {
	t.Parallel()
	show := mustWatchlistItem(t, "plex://show/pilot", acquisition.WatchlistMediaTypeShow)
	observation, err := acquisition.NewWatchlistObservation(show, true, 1, 1)
	require.NoError(t, err)

	claim, err := acquisition.NewWatchlistIntentClaim(observation, 4, 2)
	require.NoError(t, err)
	request, err := claim.SearchRequest()

	require.NoError(t, err)
	require.Equal(t, "Example Movie S01E01", request.Query())
	require.Equal(t, 1, claim.Season())
	require.Equal(t, 1, claim.Episode())
	require.True(t, claim.AutoEligible())

	jobClaim, err := acquisition.NewWatchlistIntentJobClaim(
		observation, 4, 2, "0123456789abcdef0123456789abcdef",
	)
	require.NoError(t, err)
	require.Equal(t, claim.Item(), jobClaim.Item())
	require.Equal(t, 1, jobClaim.Season())
	require.Equal(t, 1, jobClaim.Episode())
}

func TestWatchlistCompletionConstructorsEnforceOutcomeContract(t *testing.T) {
	t.Parallel()
	nextAttempt := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

	succeeded, err := acquisition.NewWatchlistSucceeded("torbox-object-17")
	require.NoError(t, err)
	require.Equal(t, acquisition.WatchlistQueueStateSucceeded, succeeded.State())
	require.Equal(t, "torbox-object-17", succeeded.PublishedObjectID())
	require.True(t, succeeded.NextAttempt().IsZero())

	for _, state := range []acquisition.WatchlistQueueState{
		acquisition.WatchlistQueueStateNotCached,
		acquisition.WatchlistQueueStateRetryable,
	} {
		completion, completionErr := acquisition.NewWatchlistDeferred(state, nextAttempt)
		require.NoError(t, completionErr)
		require.Equal(t, state, completion.State())
		require.Equal(t, nextAttempt, completion.NextAttempt())
		require.Empty(t, completion.PublishedObjectID())
	}

	manual := acquisition.NewWatchlistManualReview()
	require.Equal(t, acquisition.WatchlistQueueStateManualReview, manual.State())

	_, err = acquisition.NewWatchlistSucceeded(" ")
	require.Error(t, err)
	_, err = acquisition.NewWatchlistSucceeded("bad\nobject")
	require.Error(t, err)
	_, err = acquisition.NewWatchlistDeferred(acquisition.WatchlistQueueStatePending, nextAttempt)
	require.Error(t, err)
	_, err = acquisition.NewWatchlistDeferred(acquisition.WatchlistQueueStateRetryable, time.Time{})
	require.Error(t, err)
	require.False(t, errors.Is(err, acquisition.ErrStaleWatchlistClaim))
}

func mustWatchlistItem(t *testing.T, externalID string, mediaType acquisition.WatchlistMediaType) acquisition.WatchlistItem {
	t.Helper()
	item, err := acquisition.NewWatchlistItem(acquisition.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: externalID, MediaType: mediaType,
		Title: "Example Movie", Year: 2026,
	})
	require.NoError(t, err)
	return item
}
