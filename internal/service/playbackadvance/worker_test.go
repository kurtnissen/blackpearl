package playbackadvance_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/domain"
	playbackadvance "github.com/kurtnissen/blackpearl/internal/service/playbackadvance"
	"github.com/stretchr/testify/require"
)

const testShowID = "plex://show/5d9c086ce98e47001eb0f520"

func TestWorkerAdvancesOneQualifyingExactPublishedEpisode(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	playback := mustPlayback(t, 1, 1, 5*time.Minute, 20*time.Minute)
	published := mustPublishedEpisode(t, 1, 1, "mariposahd/episode-1.mp4")
	snapshotter := &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback}}
	index := &fakePublishedEpisodeIndex{configuration: published, found: true}
	frontier := &fakeEpisodeFrontier{eligible: true, advanceResults: []bool{true}}
	resolver := &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)}
	worker := newWorker(t, snapshotter, index, frontier, resolver, now)

	count, err := worker.Process(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, playback.VirtualPath(), index.path)
	require.Equal(t, testShowID, frontier.externalID)
	require.Equal(t, published.Backing().ObjectID, frontier.objectID)
	require.Equal(t, mustCoordinate(t, 1, 1), frontier.current)
	require.Equal(t, mustCoordinate(t, 1, 2), frontier.next)
	require.Equal(t, now.Add(-2*time.Minute), frontier.observedAfter)
	require.Equal(t, now, frontier.now)
}

func TestWorkerIgnoresPlaybackThatCannotIdentifyOneEligibleFrontier(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	qualifying := mustPlayback(t, 1, 1, 5*time.Minute, 20*time.Minute)
	published := mustPublishedEpisode(t, 1, 1, "mariposahd/episode-1.mp4")
	tests := []struct {
		name        string
		playback    domain.EpisodePlayback
		index       *fakePublishedEpisodeIndex
		frontier    *fakeEpisodeFrontier
		resolver    *fakeNextEpisodeResolver
		wantCanCall bool
	}{
		{
			name:     "below time threshold",
			playback: mustPlayback(t, 1, 1, 119*time.Second, 10*time.Minute),
			index:    &fakePublishedEpisodeIndex{configuration: published, found: true},
			frontier: &fakeEpisodeFrontier{eligible: true},
			resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
		},
		{
			name:     "below percent threshold",
			playback: mustPlayback(t, 1, 1, 2*time.Minute, 30*time.Minute),
			index:    &fakePublishedEpisodeIndex{configuration: published, found: true},
			frontier: &fakeEpisodeFrontier{eligible: true},
			resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
		},
		{
			name: "missing manifest path", playback: qualifying,
			index: &fakePublishedEpisodeIndex{}, frontier: &fakeEpisodeFrontier{eligible: true},
			resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
		},
		{
			name: "manifest coordinate mismatch", playback: qualifying,
			index:    &fakePublishedEpisodeIndex{configuration: mustPublishedEpisode(t, 1, 2, "mariposahd/episode-2.mp4"), found: true},
			frontier: &fakeEpisodeFrontier{eligible: true}, resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
		},
		{
			name: "ineligible frontier", playback: qualifying,
			index:    &fakePublishedEpisodeIndex{configuration: published, found: true},
			frontier: &fakeEpisodeFrontier{eligible: false}, resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
			wantCanCall: true,
		},
		{
			name: "terminal show", playback: qualifying,
			index:    &fakePublishedEpisodeIndex{configuration: published, found: true},
			frontier: &fakeEpisodeFrontier{eligible: true}, resolver: &fakeNextEpisodeResolver{err: domain.ErrNotFound},
			wantCanCall: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := newWorker(t, &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{test.playback}}, test.index, test.frontier, test.resolver, now)

			count, err := worker.Process(context.Background())

			require.NoError(t, err)
			require.Zero(t, count)
			require.Equal(t, test.wantCanCall, test.frontier.canCalls > 0)
			require.Zero(t, test.frontier.advanceCalls)
		})
	}
}

func TestWorkerCountsOnlyOneOptimisticWinnerForDuplicateSessions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	playback := mustPlayback(t, 1, 1, 5*time.Minute, 20*time.Minute)
	frontier := &fakeEpisodeFrontier{eligible: true, advanceResults: []bool{true, false}}
	worker := newWorker(
		t,
		&fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback, playback}},
		&fakePublishedEpisodeIndex{configuration: mustPublishedEpisode(t, 1, 1, "mariposahd/episode-1.mp4"), found: true},
		frontier,
		&fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)},
		now,
	)

	count, err := worker.Process(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 2, frontier.advanceCalls)
}

func TestWorkerSanitizesBoundaryFailures(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	privateErr := errors.New("private server and credential detail")
	playback := mustPlayback(t, 1, 1, 5*time.Minute, 20*time.Minute)
	published := mustPublishedEpisode(t, 1, 1, "mariposahd/episode-1.mp4")
	tests := []struct {
		name        string
		snapshotter *fakePlaybackSnapshotter
		index       *fakePublishedEpisodeIndex
		frontier    *fakeEpisodeFrontier
		resolver    *fakeNextEpisodeResolver
	}{
		{name: "playback", snapshotter: &fakePlaybackSnapshotter{err: privateErr}, index: &fakePublishedEpisodeIndex{}, frontier: &fakeEpisodeFrontier{}, resolver: &fakeNextEpisodeResolver{}},
		{name: "manifest", snapshotter: &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback}}, index: &fakePublishedEpisodeIndex{err: privateErr}, frontier: &fakeEpisodeFrontier{}, resolver: &fakeNextEpisodeResolver{}},
		{name: "frontier read", snapshotter: &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback}}, index: &fakePublishedEpisodeIndex{configuration: published, found: true}, frontier: &fakeEpisodeFrontier{canErr: privateErr}, resolver: &fakeNextEpisodeResolver{}},
		{name: "metadata", snapshotter: &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback}}, index: &fakePublishedEpisodeIndex{configuration: published, found: true}, frontier: &fakeEpisodeFrontier{eligible: true}, resolver: &fakeNextEpisodeResolver{err: privateErr}},
		{name: "frontier update", snapshotter: &fakePlaybackSnapshotter{items: []domain.EpisodePlayback{playback}}, index: &fakePublishedEpisodeIndex{configuration: published, found: true}, frontier: &fakeEpisodeFrontier{eligible: true, advanceErr: privateErr}, resolver: &fakeNextEpisodeResolver{next: mustCoordinate(t, 1, 2)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := newWorker(t, test.snapshotter, test.index, test.frontier, test.resolver, now)

			_, err := worker.Process(context.Background())

			require.ErrorIs(t, err, playbackadvance.ErrUnavailable)
			require.NotContains(t, err.Error(), privateErr.Error())
		})
	}
}

func TestWorkerPreservesCancellationAndSerializesConcurrentProcessing(t *testing.T) {
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	snapshotter := &fakePlaybackSnapshotter{delay: 15 * time.Millisecond}
	worker := newWorker(t, snapshotter, &fakePublishedEpisodeIndex{}, &fakeEpisodeFrontier{}, &fakeNextEpisodeResolver{}, now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := worker.Process(ctx)
	require.ErrorIs(t, err, context.Canceled)

	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			_, _ = worker.Process(context.Background())
		}()
	}
	group.Wait()
	require.Equal(t, int32(1), snapshotter.maximumConcurrent.Load())
}

func TestWorkerRunReportsSanitizedFailuresAndContinuesUntilCancellation(t *testing.T) {
	t.Parallel()
	reported := make(chan error, 1)
	worker, err := playbackadvance.NewWorker(
		&fakePlaybackSnapshotter{err: errors.New("private Plex endpoint detail")},
		&fakePublishedEpisodeIndex{},
		&fakeEpisodeFrontier{},
		&fakeNextEpisodeResolver{},
		playbackadvance.WorkerOptions{
			PollInterval: time.Hour, OperationTimeout: time.Second, WatchlistPollInterval: time.Minute,
			OnError: func(runErr error) { reported <- runErr },
		},
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()

	select {
	case runErr := <-reported:
		require.ErrorIs(t, runErr, playbackadvance.ErrUnavailable)
		require.NotContains(t, runErr.Error(), "private")
	case <-time.After(time.Second):
		t.Fatal("playback failure was not reported")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback worker did not stop")
	}
}

func TestWorkerValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()
	options := playbackadvance.WorkerOptions{
		PollInterval: time.Second, OperationTimeout: time.Second, WatchlistPollInterval: time.Second,
	}
	_, err := playbackadvance.NewWorker(nil, &fakePublishedEpisodeIndex{}, &fakeEpisodeFrontier{}, &fakeNextEpisodeResolver{}, options)
	require.Error(t, err)
	_, err = playbackadvance.NewWorker(&fakePlaybackSnapshotter{}, &fakePublishedEpisodeIndex{}, &fakeEpisodeFrontier{}, &fakeNextEpisodeResolver{}, playbackadvance.WorkerOptions{})
	require.Error(t, err)
}

func newWorker(
	t *testing.T,
	snapshotter *fakePlaybackSnapshotter,
	index *fakePublishedEpisodeIndex,
	frontier *fakeEpisodeFrontier,
	resolver *fakeNextEpisodeResolver,
	now time.Time,
) *playbackadvance.Worker {
	t.Helper()
	worker, err := playbackadvance.NewWorker(snapshotter, index, frontier, resolver, playbackadvance.WorkerOptions{
		PollInterval: time.Second, OperationTimeout: time.Second, WatchlistPollInterval: time.Minute,
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return worker
}

func mustPlayback(t *testing.T, season int, episode int, offset time.Duration, duration time.Duration) domain.EpisodePlayback {
	t.Helper()
	published := mustPublishedEpisode(t, season, episode, "mariposahd/episode.mp4")
	virtualPath, err := published.VirtualPath()
	require.NoError(t, err)
	playback, err := domain.NewEpisodePlayback(
		testShowID, virtualPath, season, episode, offset, duration, domain.PlaybackStatePlaying,
	)
	require.NoError(t, err)
	return playback
}

func mustPublishedEpisode(t *testing.T, season int, episode int, objectID string) domain.SetupConfiguration {
	t.Helper()
	backing, err := domain.NewBackingRef("internet-archive-file", objectID)
	require.NoError(t, err)
	candidate, err := domain.NewProviderMediaCandidate(backing, "episode.mp4", 175_099_607)
	require.NoError(t, err)
	configuration, err := domain.NewSetupEpisodeConfiguration(candidate, "MariposaHD", 2006, season, episode, "Episode")
	require.NoError(t, err)
	return configuration
}

func mustCoordinate(t *testing.T, season int, episode int) domain.EpisodeCoordinate {
	t.Helper()
	coordinate, err := domain.NewEpisodeCoordinate(season, episode)
	require.NoError(t, err)
	return coordinate
}

type fakePlaybackSnapshotter struct {
	items []domain.EpisodePlayback
	err   error
	delay time.Duration

	concurrent        atomic.Int32
	maximumConcurrent atomic.Int32
}

func (f *fakePlaybackSnapshotter) Snapshot(ctx context.Context) ([]domain.EpisodePlayback, error) {
	current := f.concurrent.Add(1)
	defer f.concurrent.Add(-1)
	for {
		maximum := f.maximumConcurrent.Load()
		if current <= maximum || f.maximumConcurrent.CompareAndSwap(maximum, current) {
			break
		}
	}
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return append([]domain.EpisodePlayback(nil), f.items...), f.err
}

type fakePublishedEpisodeIndex struct {
	configuration domain.SetupConfiguration
	found         bool
	err           error
	path          string
}

func (f *fakePublishedEpisodeIndex) FindPublishedEpisode(_ context.Context, virtualPath string) (domain.SetupConfiguration, bool, error) {
	f.path = virtualPath
	return f.configuration, f.found, f.err
}

type fakeEpisodeFrontier struct {
	eligible       bool
	canErr         error
	advanceResults []bool
	advanceErr     error

	canCalls      int
	advanceCalls  int
	externalID    string
	objectID      string
	current       domain.EpisodeCoordinate
	next          domain.EpisodeCoordinate
	observedAfter time.Time
	now           time.Time
}

func (f *fakeEpisodeFrontier) CanAdvanceEpisode(
	_ context.Context,
	_ string,
	externalID string,
	objectID string,
	current domain.EpisodeCoordinate,
	observedAfter time.Time,
) (bool, error) {
	f.canCalls++
	f.externalID = externalID
	f.objectID = objectID
	f.current = current
	f.observedAfter = observedAfter
	return f.eligible, f.canErr
}

func (f *fakeEpisodeFrontier) AdvanceEpisode(
	_ context.Context,
	_ string,
	externalID string,
	objectID string,
	current domain.EpisodeCoordinate,
	next domain.EpisodeCoordinate,
	observedAfter time.Time,
	now time.Time,
) (bool, error) {
	f.advanceCalls++
	f.externalID = externalID
	f.objectID = objectID
	f.current = current
	f.next = next
	f.observedAfter = observedAfter
	f.now = now
	if len(f.advanceResults) == 0 {
		return false, f.advanceErr
	}
	result := f.advanceResults[0]
	f.advanceResults = f.advanceResults[1:]
	return result, f.advanceErr
}

type fakeNextEpisodeResolver struct {
	next domain.EpisodeCoordinate
	err  error
}

func (f *fakeNextEpisodeResolver) Next(_ context.Context, _ string, _ domain.EpisodeCoordinate) (domain.EpisodeCoordinate, error) {
	return f.next, f.err
}
