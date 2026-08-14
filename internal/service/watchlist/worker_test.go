package watchlist_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	watchlistservice "github.com/blackpearl-media/blackpearl/internal/service/watchlist"
	"github.com/stretchr/testify/require"
)

const testJobID = "0123456789abcdef0123456789abcdef"

func TestWorkerSubmitsAndDurablyAttachesNewWatchlistJob(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	claim := mustClaim(t, "plex://movie/submit", 1, "")
	queue := &fakeWorkerQueue{claims: []acquisition.WatchlistClaim{claim}}
	manager := &fakeJobManager{submitJob: mustJob(t, claim, acquisition.JobStateQueued, acquisition.JobErrorNone)}
	worker := newWorker(t, queue, manager, now)

	state, err := worker.ProcessOne(context.Background())

	require.NoError(t, err)
	require.Equal(t, acquisition.WatchlistQueueStateAcquiring, state)
	require.Equal(t, claim.Item().Title(), manager.submitted.Title())
	require.Equal(t, testJobID, queue.attachedJobID)
	require.Equal(t, now.Add(30*time.Second), queue.nextAttempt)
	require.Empty(t, queue.completions)
}

func TestWorkerDefersActiveDurableJobWithoutResubmitting(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	claim := mustClaim(t, "plex://movie/active", 2, testJobID)
	queue := &fakeWorkerQueue{claims: []acquisition.WatchlistClaim{claim}}
	manager := &fakeJobManager{getJob: mustJob(t, claim, acquisition.JobStatePreparing, acquisition.JobErrorNone)}
	worker := newWorker(t, queue, manager, now)

	state, err := worker.ProcessOne(context.Background())

	require.NoError(t, err)
	require.Equal(t, acquisition.WatchlistQueueStateAcquiring, state)
	require.Zero(t, manager.submitCalls)
	require.Equal(t, testJobID, manager.gotID)
	require.Equal(t, now.Add(30*time.Second), queue.nextAttempt)
}

func TestWorkerCompletesSucceededDurableJob(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	claim := mustClaim(t, "plex://movie/success", 2, testJobID)
	queue := &fakeWorkerQueue{claims: []acquisition.WatchlistClaim{claim}}
	manager := &fakeJobManager{getJob: mustJob(t, claim, acquisition.JobStateSucceeded, acquisition.JobErrorNone)}
	worker := newWorker(t, queue, manager, now)

	state, err := worker.ProcessOne(context.Background())

	require.NoError(t, err)
	require.Equal(t, acquisition.WatchlistQueueStateSucceeded, state)
	require.Len(t, queue.completions, 1)
	require.Equal(t, "torbox-object-17", queue.completions[0].PublishedObjectID())
}

func TestWorkerMapsDurableTerminalOutcomes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 16, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		state acquisition.JobState
		code  acquisition.JobErrorCode
		want  acquisition.WatchlistQueueState
		next  time.Time
	}{
		{name: "no release", state: acquisition.JobStateFailed, code: acquisition.JobErrorNoRelease, want: acquisition.WatchlistQueueStateNotCached, next: now.Add(6 * time.Hour)},
		{name: "stalled", state: acquisition.JobStateFailed, code: acquisition.JobErrorStalled, want: acquisition.WatchlistQueueStateNotCached, next: now.Add(6 * time.Hour)},
		{name: "unauthorized", state: acquisition.JobStateFailed, code: acquisition.JobErrorUnauthorized, want: acquisition.WatchlistQueueStateRetryable, next: now.Add(15 * time.Minute)},
		{name: "unplayable", state: acquisition.JobStateFailed, code: acquisition.JobErrorNoPlayableMedia, want: acquisition.WatchlistQueueStateManualReview},
		{name: "ambiguous", state: acquisition.JobStateManualReview, code: acquisition.JobErrorAmbiguousMutation, want: acquisition.WatchlistQueueStateManualReview},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := mustClaim(t, "plex://movie/"+test.name, 2, testJobID)
			queue := &fakeWorkerQueue{claims: []acquisition.WatchlistClaim{claim}}
			manager := &fakeJobManager{getJob: mustJob(t, claim, test.state, test.code)}
			worker := newWorker(t, queue, manager, now)

			state, err := worker.ProcessOne(context.Background())

			require.NoError(t, err)
			require.Equal(t, test.want, state)
			require.Equal(t, test.want, queue.completions[0].State())
			require.Equal(t, test.next, queue.completions[0].NextAttempt())
		})
	}
}

func TestWorkerAttachesSubmittedJobAfterRequestCancellation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	claim := mustClaim(t, "plex://movie/cancel", 1, "")
	queue := &fakeWorkerQueue{claims: []acquisition.WatchlistClaim{claim}}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &fakeJobManager{
		submitJob:   mustJob(t, claim, acquisition.JobStateQueued, acquisition.JobErrorNone),
		afterSubmit: cancel,
	}
	worker := newWorker(t, queue, manager, now)

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.WatchlistQueueStateAcquiring, state)
	require.False(t, queue.transitionContextCanceled)
}

func TestWorkerSerializesConcurrentProcessing(t *testing.T) {
	claims := []acquisition.WatchlistClaim{
		mustClaim(t, "plex://movie/one", 1, ""),
		mustClaim(t, "plex://movie/two", 1, ""),
	}
	queue := &fakeWorkerQueue{claims: claims}
	manager := &fakeJobManager{submitJob: mustJob(t, claims[0], acquisition.JobStateQueued, acquisition.JobErrorNone), delay: 10 * time.Millisecond}
	worker := newWorker(t, queue, manager, time.Now())
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			_, _ = worker.ProcessOne(context.Background())
		}()
	}
	group.Wait()

	require.Equal(t, int32(1), manager.maximumConcurrent.Load())
	require.Equal(t, 2, manager.submitCalls)
}

func TestWorkerReportsNoWorkAndValidatesOptions(t *testing.T) {
	t.Parallel()
	queue := &fakeWorkerQueue{}
	manager := &fakeJobManager{}
	worker := newWorker(t, queue, manager, time.Now())

	_, err := worker.ProcessOne(context.Background())
	require.ErrorIs(t, err, domain.ErrNotFound)

	valid := watchlistservice.WorkerOptions{
		LeaseDuration: time.Minute, OperationTimeout: 10 * time.Second, IdleInterval: time.Second,
		ReconcileInterval: 30 * time.Second, NotCachedCooldown: time.Hour, RetryCooldown: time.Minute,
	}
	_, err = watchlistservice.NewWorker(nil, manager, valid)
	require.Error(t, err)
	_, err = watchlistservice.NewWorker(queue, nil, valid)
	require.Error(t, err)
	_, err = watchlistservice.NewWorker(queue, manager, watchlistservice.WorkerOptions{})
	require.Error(t, err)
}

func newWorker(t *testing.T, queue *fakeWorkerQueue, manager *fakeJobManager, now time.Time) *watchlistservice.Worker {
	t.Helper()
	worker, err := watchlistservice.NewWorker(queue, manager, watchlistservice.WorkerOptions{
		LeaseDuration: time.Minute, OperationTimeout: 10 * time.Second, IdleInterval: time.Millisecond,
		ReconcileInterval: 30 * time.Second, NotCachedCooldown: 6 * time.Hour,
		RetryCooldown: 15 * time.Minute, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return worker
}

func mustClaim(t *testing.T, externalID string, attempt int, jobID string) acquisition.WatchlistClaim {
	t.Helper()
	item := mustObserverItem(t, externalID)
	var (
		claim acquisition.WatchlistClaim
		err   error
	)
	if jobID == "" {
		claim, err = acquisition.NewWatchlistClaim(item, int64(attempt), attempt)
	} else {
		claim, err = acquisition.NewWatchlistJobClaim(item, int64(attempt), attempt, jobID)
	}
	require.NoError(t, err)
	return claim
}

func mustJob(t *testing.T, claim acquisition.WatchlistClaim, state acquisition.JobState, code acquisition.JobErrorCode) acquisition.AcquisitionJob {
	t.Helper()
	request, err := claim.Item().SearchRequest()
	require.NoError(t, err)
	now := time.Date(2026, time.August, 14, 15, 0, 0, 0, time.UTC)
	input := acquisition.JobSnapshotInput{ID: testJobID, Request: request, State: state, ErrorCode: code, CreatedAt: now, UpdatedAt: now}
	if state != acquisition.JobStateQueued {
		release, releaseErr := acquisition.NewRelease(acquisition.ReleaseInput{
			Provider: "prowlarr", SourceID: "release", Title: request.Query(), Protocol: acquisition.ReleaseProtocolTorrent,
			Size: 20, Indexer: "authorized", InfoHash: "0123456789abcdef0123456789abcdef01234567",
		})
		require.NoError(t, releaseErr)
		selection, selectionErr := acquisition.NewJobSelection(release)
		require.NoError(t, selectionErr)
		input.Selection = &selection
	}
	if state == acquisition.JobStatePreparing || state == acquisition.JobStateSucceeded {
		created, createdErr := acquisition.NewCreatedObject("torbox", "17")
		require.NoError(t, createdErr)
		input.CreatedObject = &created
	}
	if state == acquisition.JobStatePreparing {
		input.Progress = 50
	}
	if state == acquisition.JobStateSucceeded {
		input.PublishedObjectID = "torbox-object-17"
		input.Progress = 100
	}
	job, err := acquisition.NewAcquisitionJobSnapshot(input)
	require.NoError(t, err)
	return job
}

type fakeWorkerQueue struct {
	mu                        sync.Mutex
	claims                    []acquisition.WatchlistClaim
	claimIndex                int
	attachedJobID             string
	nextAttempt               time.Time
	completions               []acquisition.WatchlistCompletion
	transitionContextCanceled bool
}

func (f *fakeWorkerQueue) Claim(ctx context.Context, _ time.Time, _ time.Duration) (acquisition.WatchlistClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return acquisition.WatchlistClaim{}, err
	}
	if f.claimIndex >= len(f.claims) {
		return acquisition.WatchlistClaim{}, domain.ErrNotFound
	}
	claim := f.claims[f.claimIndex]
	f.claimIndex++
	return claim, nil
}

func (f *fakeWorkerQueue) AttachJob(ctx context.Context, _ acquisition.WatchlistClaim, jobID string, nextAttempt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitionContextCanceled = ctx.Err() != nil
	f.attachedJobID = jobID
	f.nextAttempt = nextAttempt
	return nil
}

func (f *fakeWorkerQueue) DeferJob(ctx context.Context, _ acquisition.WatchlistClaim, nextAttempt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitionContextCanceled = ctx.Err() != nil
	f.nextAttempt = nextAttempt
	return nil
}

func (f *fakeWorkerQueue) Complete(ctx context.Context, _ acquisition.WatchlistClaim, completion acquisition.WatchlistCompletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitionContextCanceled = ctx.Err() != nil
	f.completions = append(f.completions, completion)
	return nil
}

type fakeJobManager struct {
	submitJob         acquisition.AcquisitionJob
	getJob            acquisition.AcquisitionJob
	err               error
	submitted         acquisition.SearchRequest
	gotID             string
	submitCalls       int
	delay             time.Duration
	afterSubmit       func()
	concurrent        atomic.Int32
	maximumConcurrent atomic.Int32
}

func (f *fakeJobManager) Submit(ctx context.Context, request acquisition.SearchRequest) (acquisition.AcquisitionJob, bool, error) {
	f.submitted = request
	f.submitCalls++
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
			return acquisition.AcquisitionJob{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	if f.afterSubmit != nil {
		f.afterSubmit()
	}
	return f.submitJob, true, f.err
}

func (f *fakeJobManager) Get(_ context.Context, id string) (acquisition.AcquisitionJob, error) {
	f.gotID = id
	return f.getJob, f.err
}
