package watchlist_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
	watchlistservice "github.com/blackpearl-media/blackpearl/internal/service/watchlist"
	"github.com/stretchr/testify/require"
)

func TestWorkerProcessOnePublishesSuccessfulMovieOutcome(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	claim := mustClaim(t, "plex://movie/success", 1)
	queue := &fakeWorkerQueue{claims: []acquisitiondomain.WatchlistClaim{claim}}
	media := mustAcquiredMedia(t, claim.Item(), "18:2")
	acquirer := &fakeMovieAcquirer{media: media}
	worker := newWorker(t, queue, acquirer, now)

	state, err := worker.ProcessOne(context.Background())

	require.NoError(t, err)
	require.Equal(t, acquisitiondomain.WatchlistQueueStateSucceeded, state)
	require.Equal(t, "18:2", queue.completions[0].PublishedObjectID())
	require.Equal(t, claim.Item().Title(), acquirer.request.Title())
	require.Equal(t, claim.Item().Year(), acquirer.request.Year())
}

func TestWorkerProcessOneMapsOnlySafeFailuresToAutomaticRetries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
		want acquisitiondomain.WatchlistQueueState
		next time.Time
	}{
		{name: "not cached", err: acquisitionservice.ErrNotCached, want: acquisitiondomain.WatchlistQueueStateNotCached, next: now.Add(6 * time.Hour)},
		{name: "provider unavailable", err: acquisitionservice.ErrUnavailable, want: acquisitiondomain.WatchlistQueueStateRetryable, next: now.Add(15 * time.Minute)},
		{name: "provider unauthorized", err: domain.ErrUnauthorized, want: acquisitiondomain.WatchlistQueueStateRetryable, next: now.Add(15 * time.Minute)},
		{name: "provider not configured", err: domain.ErrNotConfigured, want: acquisitiondomain.WatchlistQueueStateRetryable, next: now.Add(15 * time.Minute)},
		{name: "ambiguous mutation", err: acquisitionservice.ErrAmbiguousMutation, want: acquisitiondomain.WatchlistQueueStateManualReview},
		{name: "created object not playable", err: acquisitionservice.ErrNoPlayableMedia, want: acquisitiondomain.WatchlistQueueStateManualReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := mustClaim(t, "plex://movie/"+test.name, 1)
			queue := &fakeWorkerQueue{claims: []acquisitiondomain.WatchlistClaim{claim}}
			worker := newWorker(t, queue, &fakeMovieAcquirer{err: test.err}, now)

			state, err := worker.ProcessOne(context.Background())

			require.NoError(t, err)
			require.Equal(t, test.want, state)
			require.Len(t, queue.completions, 1)
			require.Equal(t, test.want, queue.completions[0].State())
			require.Equal(t, test.next, queue.completions[0].NextAttempt())
		})
	}
}

func TestWorkerRecordsAmbiguousMutationEvenWhenRequestWasCanceled(t *testing.T) {
	t.Parallel()
	claim := mustClaim(t, "plex://movie/ambiguous-cancel", 1)
	queue := &fakeWorkerQueue{claims: []acquisitiondomain.WatchlistClaim{claim}}
	worker := newWorker(t, queue, &fakeMovieAcquirer{err: errors.Join(acquisitionservice.ErrAmbiguousMutation, context.Canceled)}, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queue.claimDespiteCancellation = true

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisitiondomain.WatchlistQueueStateManualReview, state)
	require.Len(t, queue.completions, 1)
	require.False(t, queue.completionContextCanceled)
}

func TestWorkerLeavesPreMutationCancellationForLeaseRecovery(t *testing.T) {
	t.Parallel()
	claim := mustClaim(t, "plex://movie/cancel", 1)
	queue := &fakeWorkerQueue{claims: []acquisitiondomain.WatchlistClaim{claim}}
	worker := newWorker(t, queue, &fakeMovieAcquirer{err: context.Canceled}, time.Now())

	_, err := worker.ProcessOne(context.Background())

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, queue.completions)
}

func TestWorkerSerializesConcurrentProcessing(t *testing.T) {
	claims := []acquisitiondomain.WatchlistClaim{
		mustClaim(t, "plex://movie/one", 1),
		mustClaim(t, "plex://movie/two", 1),
	}
	queue := &fakeWorkerQueue{claims: claims}
	acquirer := &fakeMovieAcquirer{media: mustAcquiredMedia(t, claims[0].Item(), "18:2"), delay: 10 * time.Millisecond}
	worker := newWorker(t, queue, acquirer, time.Now())
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			_, _ = worker.ProcessOne(context.Background())
		}()
	}
	group.Wait()

	require.Equal(t, int32(1), acquirer.maximumConcurrent.Load())
	require.Len(t, queue.completions, 2)
}

func TestWorkerReportsNoWorkAndValidatesOptions(t *testing.T) {
	t.Parallel()
	queue := &fakeWorkerQueue{}
	acquirer := &fakeMovieAcquirer{}
	worker := newWorker(t, queue, acquirer, time.Now())

	_, err := worker.ProcessOne(context.Background())
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = watchlistservice.NewWorker(nil, acquirer, watchlistservice.WorkerOptions{LeaseDuration: time.Minute, AcquisitionTimeout: time.Minute, IdleInterval: time.Minute, NotCachedCooldown: time.Minute, RetryCooldown: time.Minute})
	require.Error(t, err)
	_, err = watchlistservice.NewWorker(queue, nil, watchlistservice.WorkerOptions{LeaseDuration: time.Minute, AcquisitionTimeout: time.Minute, IdleInterval: time.Minute, NotCachedCooldown: time.Minute, RetryCooldown: time.Minute})
	require.Error(t, err)
	_, err = watchlistservice.NewWorker(queue, acquirer, watchlistservice.WorkerOptions{})
	require.Error(t, err)
}

func newWorker(t *testing.T, queue *fakeWorkerQueue, acquirer *fakeMovieAcquirer, now time.Time) *watchlistservice.Worker {
	t.Helper()
	worker, err := watchlistservice.NewWorker(queue, acquirer, watchlistservice.WorkerOptions{
		LeaseDuration:      10 * time.Minute,
		AcquisitionTimeout: time.Minute,
		IdleInterval:       time.Millisecond,
		NotCachedCooldown:  6 * time.Hour,
		RetryCooldown:      15 * time.Minute,
		Now:                func() time.Time { return now },
	})
	require.NoError(t, err)
	return worker
}

func mustClaim(t *testing.T, externalID string, attempt int) acquisitiondomain.WatchlistClaim {
	t.Helper()
	item := mustObserverItem(t, externalID)
	claim, err := acquisitiondomain.NewWatchlistClaim(item, int64(attempt), attempt)
	require.NoError(t, err)
	return claim
}

func mustAcquiredMedia(t *testing.T, item acquisitiondomain.WatchlistItem, objectID string) acquisitiondomain.AcquiredMedia {
	t.Helper()
	request, err := item.SearchRequest()
	require.NoError(t, err)
	release, err := acquisitiondomain.NewRelease(acquisitiondomain.ReleaseInput{
		Provider: "prowlarr", SourceID: "release", Title: request.Query(), Protocol: acquisitiondomain.ReleaseProtocolTorrent,
		Size: 20, Indexer: "authorized", InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate(objectID, "Example.2026.mkv", 20)
	require.NoError(t, err)
	media, err := acquisitiondomain.NewAcquiredMedia(request, release, candidate)
	require.NoError(t, err)
	return media
}

type fakeWorkerQueue struct {
	mu                        sync.Mutex
	claims                    []acquisitiondomain.WatchlistClaim
	claimIndex                int
	claimErr                  error
	claimDespiteCancellation  bool
	completions               []acquisitiondomain.WatchlistCompletion
	completionErr             error
	completionContextCanceled bool
}

func (f *fakeWorkerQueue) Claim(ctx context.Context, _ time.Time, _ time.Duration) (acquisitiondomain.WatchlistClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil && !f.claimDespiteCancellation {
		return acquisitiondomain.WatchlistClaim{}, err
	}
	if f.claimErr != nil {
		return acquisitiondomain.WatchlistClaim{}, f.claimErr
	}
	if f.claimIndex >= len(f.claims) {
		return acquisitiondomain.WatchlistClaim{}, domain.ErrNotFound
	}
	claim := f.claims[f.claimIndex]
	f.claimIndex++
	return claim, nil
}

func (f *fakeWorkerQueue) Complete(ctx context.Context, _ acquisitiondomain.WatchlistClaim, completion acquisitiondomain.WatchlistCompletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completionContextCanceled = ctx.Err() != nil
	f.completions = append(f.completions, completion)
	return f.completionErr
}

type fakeMovieAcquirer struct {
	media             acquisitiondomain.AcquiredMedia
	err               error
	request           acquisitiondomain.SearchRequest
	delay             time.Duration
	concurrent        atomic.Int32
	maximumConcurrent atomic.Int32
}

func (f *fakeMovieAcquirer) Acquire(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error) {
	f.request = request
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
			return acquisitiondomain.AcquiredMedia{}, ctx.Err()
		case <-timer.C:
		}
	}
	return f.media, f.err
}
