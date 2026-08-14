package acquisitionjob_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionjobrepo "github.com/blackpearl-media/blackpearl/internal/repository/acquisitionjob"
	acquisitionjobservice "github.com/blackpearl-media/blackpearl/internal/service/acquisitionjob"
	"github.com/stretchr/testify/require"
)

func TestWorkerPersistsSelectionInOneLeaseBeforeAnyProviderMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := queuedJob(t, ctx)
	provider := &fakeJobProvider{releases: []acquisition.Release{mustJobRelease(t)}}
	publisher := &fakeJobPublisher{}
	worker := newJobWorker(t, repository, provider, publisher, now)

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Zero(t, provider.findCalls)
	require.Zero(t, provider.createCalls)
	selected, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, mustJobRelease(t).InfoHash(), selected.Selection().InfoHash())
}

func TestWorkerPlansCachedFirstBoundedCandidateSetBeforeAnyProviderMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := queuedJob(t, ctx)
	releases := []acquisition.Release{
		mustJobReleaseAt(t, 0), mustJobReleaseAt(t, 1), mustJobReleaseAt(t, 2),
		mustJobReleaseAt(t, 2), mustJobReleaseAt(t, 3), mustJobReleaseAt(t, 4),
		mustJobReleaseAt(t, 5),
	}
	provider := &fakeJobProvider{
		releases: releases,
		cached: []acquisition.Release{
			mustJobReleaseAt(t, 2), mustJobReleaseAt(t, 4), mustJobReleaseAt(t, 5),
		},
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now)

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Equal(t, 1, provider.cacheCalls)
	require.Len(t, provider.cacheInput, 6)
	require.Zero(t, provider.findCalls)
	require.Zero(t, provider.createCalls)
	candidates, err := repository.Candidates(ctx, jobID)
	require.NoError(t, err)
	require.Len(t, candidates, acquisition.MaximumJobCandidates)
	require.Equal(t, []string{
		mustJobReleaseAt(t, 2).InfoHash(),
		mustJobReleaseAt(t, 4).InfoHash(),
		mustJobReleaseAt(t, 5).InfoHash(),
		mustJobReleaseAt(t, 0).InfoHash(),
		mustJobReleaseAt(t, 1).InfoHash(),
	}, candidateHashes(candidates))
	require.Equal(t, acquisition.CandidateOutcomeSelected, candidates[0].Outcome())
	for _, candidate := range candidates[1:] {
		require.Equal(t, acquisition.CandidateOutcomePending, candidate.Outcome())
	}
}

func TestWorkerBoundsCacheProbeBeforeProviderMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, _ := queuedJob(t, ctx)
	releases := make([]acquisition.Release, 0, 101)
	for ordinal := range 101 {
		release, err := acquisition.NewRelease(acquisition.ReleaseInput{
			Provider: "prowlarr", SourceID: fmt.Sprintf("result-%d", ordinal), Title: "Example.Movie.2026",
			Protocol: acquisition.ReleaseProtocolTorrent, Size: int64(100 + ordinal), Indexer: "authorized-indexer",
			InfoHash: fmt.Sprintf("%040x", ordinal+1), MagnetURL: fmt.Sprintf("magnet:?xt=urn:btih:%040x", ordinal+1),
		})
		require.NoError(t, err)
		releases = append(releases, release)
	}
	provider := &fakeJobProvider{releases: releases}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now)

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Len(t, provider.cacheInput, 100)
	require.Zero(t, provider.findCalls)
	require.Zero(t, provider.createCalls)
}

func TestWorkerDefersCandidatePlanningWhenCacheLookupIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := queuedJob(t, ctx)
	provider := &fakeJobProvider{
		releases: []acquisition.Release{mustJobRelease(t)},
		cacheErr: errors.New("private cache lookup failed"),
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now)

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateQueued, state)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobErrorProviderUnavailable, job.ErrorCode())
	candidates, err := repository.Candidates(ctx, jobID)
	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestWorkerReconcilesSelectedHashBeforeCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := selectedJob(t, ctx)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	provider := &fakeJobProvider{found: created}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(2*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	require.Equal(t, 1, provider.findCalls)
	require.Zero(t, provider.materializeCalls)
	require.Zero(t, provider.createCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, "17", job.CreatedObject().ObjectID())
	require.False(t, job.CreatedByJob())
}

func TestWorkerCreatesMissingSelectedReleaseThenAttachesObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := selectedJob(t, ctx)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "18")
	require.NoError(t, err)
	provider := &fakeJobProvider{
		releases: []acquisition.Release{mustJobRelease(t)}, findErrs: []error{domain.ErrNotFound},
		material: mustJobTorrentInput(t), created: created,
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(2*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	require.Equal(t, 1, provider.materializeCalls)
	require.Equal(t, 1, provider.createCalls)
	require.True(t, provider.allowDownload)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.True(t, job.CreatedByJob())
}

func TestWorkerReconcilesAmbiguousCreateResponseWithoutSecondCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := selectedJob(t, ctx)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "19")
	require.NoError(t, err)
	provider := &fakeJobProvider{
		releases: []acquisition.Release{mustJobRelease(t)},
		findErrs: []error{domain.ErrNotFound, nil}, foundSequence: []acquisition.CreatedObject{{}, created},
		material: mustJobTorrentInput(t), createErr: errors.New("ambiguous private provider response"),
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(2*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	require.Equal(t, 2, provider.findCalls)
	require.Equal(t, 1, provider.createCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.True(t, job.CreatedByJob())
}

func TestWorkerStopsAutomaticRetryWhenCreateCannotBeReconciled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := selectedJob(t, ctx)
	provider := &fakeJobProvider{
		releases: []acquisition.Release{mustJobRelease(t)},
		findErrs: []error{domain.ErrNotFound, domain.ErrNotFound},
		material: mustJobTorrentInput(t), createErr: errors.New("ambiguous private provider response"),
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(2*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateManualReview, state)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobErrorAmbiguousMutation, job.ErrorCode())
	_, err = repository.Claim(ctx, now.Add(time.Hour), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestWorkerDefersNotReadyAndPublishesReadyMediaOnLaterLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := preparingJob(t, ctx)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.Movie.2026.mp4", 100)
	require.NoError(t, err)
	provider := &fakeJobProvider{inspectionErrs: []error{acquisition.ErrNotReady, nil}, candidates: []domain.MediaCandidate{candidate}}
	publisher := &fakeJobPublisher{}
	worker := newJobWorker(t, repository, provider, publisher, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	require.Empty(t, publisher.published)
	_, err = repository.Claim(ctx, now.Add(8*time.Second), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)

	worker = newJobWorker(t, repository, provider, publisher, now.Add(15*time.Second))
	state, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSucceeded, state)
	require.Len(t, publisher.published, 1)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, "17:3", job.PublishedObjectID())
}

func TestWorkerPersistsMonotonicPreparationProgressAcrossPolls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := preparingJob(t, ctx)
	provider := &fakeJobProvider{
		inspectionProgress: []int{37, 22},
		inspectionErrs:     []error{acquisition.ErrNotReady, acquisition.ErrNotReady},
	}

	state, err := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second)).ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, 37, job.Progress())

	state, err = newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(15*time.Second)).ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	job, err = repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, 37, job.Progress())
}

func TestWorkerClassifiesNoReleaseAndNoPlayableMediaAsTerminal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no release", func(t *testing.T) {
		repository, now, jobID := queuedJob(t, ctx)
		worker := newJobWorker(t, repository, &fakeJobProvider{}, &fakeJobPublisher{}, now)
		state, err := worker.ProcessOne(ctx)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobStateFailed, state)
		job, err := repository.Get(ctx, jobID)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobErrorNoRelease, job.ErrorCode())
	})

	t.Run("no playable media", func(t *testing.T) {
		repository, now, jobID := preparingJob(t, ctx)
		worker := newJobWorker(t, repository, &fakeJobProvider{}, &fakeJobPublisher{}, now.Add(4*time.Second))
		state, err := worker.ProcessOne(ctx)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobStateFailed, state)
		job, err := repository.Get(ctx, jobID)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobErrorNoPlayableMedia, job.ErrorCode())
	})

	t.Run("stalled without seeds", func(t *testing.T) {
		repository, now, jobID := preparingJob(t, ctx)
		provider := &fakeJobProvider{inspectionErrs: []error{acquisition.ErrStalled}}
		worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))
		state, err := worker.ProcessOne(ctx)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobStateFailed, state)
		job, err := repository.Get(ctx, jobID)
		require.NoError(t, err)
		require.Equal(t, acquisition.JobErrorStalled, job.ErrorCode())
	})
}

func TestWorkerFallsBackWithoutDeletingExistingAccountObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedPreparingJob(t, ctx, 2, false)
	provider := &fakeJobProvider{
		inspectionProgress: []int{37, 37},
		inspectionErrs:     []error{acquisition.ErrNotReady, acquisition.ErrStalled},
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, 37, job.Progress())

	worker = newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(15*time.Second))
	state, err = worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Zero(t, provider.deleteCalls)
	job, err = repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, mustJobReleaseAt(t, 1).InfoHash(), job.Selection().InfoHash())
	require.False(t, job.HasCreatedObject())
	require.Zero(t, job.Progress())
}

func TestWorkerCleansOwnedObjectBeforeFallingBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedPreparingJob(t, ctx, 2, true)
	provider := &fakeJobProvider{inspectionErrs: []error{acquisition.ErrStalled}}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Equal(t, 1, provider.deleteCalls)
	require.Equal(t, "17", provider.deleted.ObjectID())
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, mustJobReleaseAt(t, 1).InfoHash(), job.Selection().InfoHash())
	require.False(t, job.CreatedByJob())
}

func TestWorkerAdvancesMissingOwnedObjectWithoutDeletingAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, _ := plannedPreparingJob(t, ctx, 2, true)
	provider := &fakeJobProvider{inspectionErrs: []error{domain.ErrNotFound}}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Zero(t, provider.deleteCalls)
}

func TestWorkerFallsBackWhenSelectedReleaseDisappearsBeforeMaterialization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedSelectedJob(t, ctx, 2)
	provider := &fakeJobProvider{
		releases: []acquisition.Release{mustJobReleaseAt(t, 1)},
		findErrs: []error{domain.ErrNotFound},
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(2*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Zero(t, provider.createCalls)
	require.Zero(t, provider.deleteCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, mustJobReleaseAt(t, 1).InfoHash(), job.Selection().InfoHash())
}

func TestWorkerCleansUnplayableOwnedObjectBeforeFallingBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, _ := plannedPreparingJob(t, ctx, 2, true)
	provider := &fakeJobProvider{}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	require.Equal(t, 1, provider.deleteCalls)
}

func TestWorkerFallsBackThenPublishesSecondCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedPreparingJob(t, ctx, 2, true)
	secondObject, err := acquisition.NewCreatedObject("torbox-torrent", "19")
	require.NoError(t, err)
	media, err := domain.NewMediaCandidate("19:3", "Example.Movie.2026.mp4", 100)
	require.NoError(t, err)
	provider := &fakeJobProvider{
		inspectionErrs: []error{acquisition.ErrStalled},
		found:          secondObject,
		candidates:     []domain.MediaCandidate{media},
	}
	publisher := &fakeJobPublisher{}

	state, err := newJobWorker(t, repository, provider, publisher, now.Add(4*time.Second)).ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, state)
	state, err = newJobWorker(t, repository, provider, publisher, now.Add(5*time.Second)).ProcessOne(ctx)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, state)
	state, err = newJobWorker(t, repository, provider, publisher, now.Add(6*time.Second)).ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSucceeded, state)
	require.Len(t, publisher.published, 1)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, "19:3", job.PublishedObjectID())
	require.Equal(t, 1, provider.deleteCalls)
}

func TestWorkerFailsExhaustedCandidatePlanAfterOwnedCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedPreparingJob(t, ctx, 1, true)
	provider := &fakeJobProvider{inspectionErrs: []error{acquisition.ErrStalled}}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateFailed, state)
	require.Equal(t, 1, provider.deleteCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobErrorStalled, job.ErrorCode())
	candidates, err := repository.Candidates(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, acquisition.CandidateOutcomeStalled, candidates[0].Outcome())
}

func TestWorkerStopsInManualReviewWhenOwnedCleanupIsUncertain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := plannedPreparingJob(t, ctx, 2, true)
	provider := &fakeJobProvider{
		inspectionErrs: []error{acquisition.ErrStalled},
		deleteErr:      errors.New("ambiguous cleanup response"),
	}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateManualReview, state)
	require.Equal(t, 1, provider.deleteCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	require.True(t, job.CreatedByJob())
	require.Equal(t, acquisition.JobErrorAmbiguousMutation, job.ErrorCode())
}

func TestWorkerPreservesLegacyTerminalBehaviorWithoutCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, now, jobID := preparingJob(t, ctx)
	provider := &fakeJobProvider{inspectionErrs: []error{acquisition.ErrStalled}}
	worker := newJobWorker(t, repository, provider, &fakeJobPublisher{}, now.Add(4*time.Second))

	state, err := worker.ProcessOne(ctx)

	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateFailed, state)
	require.Zero(t, provider.deleteCalls)
	job, err := repository.Get(ctx, jobID)
	require.NoError(t, err)
	_, hasPlan := job.SelectedCandidateOrdinal()
	require.False(t, hasPlan)
}

func queuedJob(t *testing.T, ctx context.Context) (*acquisitionjobrepo.Repository, time.Time, string) {
	t.Helper()
	repository, err := acquisitionjobrepo.Open(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustJobMovieRequest(t), now)
	require.NoError(t, err)
	return repository, now, job.ID()
}

func selectedJob(t *testing.T, ctx context.Context) (*acquisitionjobrepo.Repository, time.Time, string) {
	t.Helper()
	repository, now, jobID := queuedJob(t, ctx)
	claim, err := repository.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Select(ctx, claim, mustJobSelection(t), now.Add(time.Second)))
	return repository, now, jobID
}

func preparingJob(t *testing.T, ctx context.Context) (*acquisitionjobrepo.Repository, time.Time, string) {
	t.Helper()
	repository, now, jobID := selectedJob(t, ctx)
	claim, err := repository.Claim(ctx, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	require.NoError(t, repository.Attach(ctx, claim, created, now.Add(3*time.Second)))
	return repository, now, jobID
}

func plannedPreparingJob(t *testing.T, ctx context.Context, count int, owned bool) (*acquisitionjobrepo.Repository, time.Time, string) {
	t.Helper()
	repository, now, jobID := plannedSelectedJob(t, ctx, count)
	claim, err := repository.Claim(ctx, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	require.NoError(t, repository.AttachPrepared(ctx, claim, created, owned, now.Add(3*time.Second)))
	return repository, now, jobID
}

func plannedSelectedJob(t *testing.T, ctx context.Context, count int) (*acquisitionjobrepo.Repository, time.Time, string) {
	t.Helper()
	repository, now, jobID := queuedJob(t, ctx)
	claim, err := repository.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	candidates := make([]acquisition.JobCandidate, 0, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		selection, selectionErr := acquisition.NewJobSelection(mustJobReleaseAt(t, ordinal))
		require.NoError(t, selectionErr)
		outcome := acquisition.CandidateOutcomePending
		if ordinal == 0 {
			outcome = acquisition.CandidateOutcomeSelected
		}
		candidate, candidateErr := acquisition.NewJobCandidate(selection, ordinal, outcome)
		require.NoError(t, candidateErr)
		candidates = append(candidates, candidate)
	}
	require.NoError(t, repository.Plan(ctx, claim, candidates, now.Add(time.Second)))
	return repository, now, jobID
}

func newJobWorker(
	t *testing.T,
	repository *acquisitionjobrepo.Repository,
	provider *fakeJobProvider,
	publisher *fakeJobPublisher,
	now time.Time,
) *acquisitionjobservice.Worker {
	t.Helper()
	worker, err := acquisitionjobservice.NewWorker(repository, func(context.Context) (acquisitionjobservice.Providers, error) {
		return acquisitionjobservice.Providers{
			Searcher: provider, Materializer: provider, Preparer: provider,
		}, nil
	}, publisher, acquisitionjobservice.WorkerOptions{
		LeaseDuration: time.Minute, OperationTimeout: 30 * time.Second,
		IdleInterval: time.Second, PreparingPollInterval: 10 * time.Second,
		RetryInterval: time.Minute, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return worker
}

type fakeJobProvider struct {
	releases           []acquisition.Release
	searchErr          error
	cached             []acquisition.Release
	cacheErr           error
	cacheInput         []acquisition.Release
	cacheCalls         int
	material           acquisition.TorrentInput
	materializeErr     error
	materializeCalls   int
	found              acquisition.CreatedObject
	foundSequence      []acquisition.CreatedObject
	findErrs           []error
	findCalls          int
	created            acquisition.CreatedObject
	createErr          error
	createCalls        int
	allowDownload      bool
	candidates         []domain.MediaCandidate
	inspectionProgress []int
	inspectionErrs     []error
	inspectionCalls    int
	deleted            acquisition.CreatedObject
	deleteErr          error
	deleteCalls        int
}

func (f *fakeJobProvider) Search(context.Context, acquisition.SearchRequest) ([]acquisition.Release, error) {
	return append([]acquisition.Release(nil), f.releases...), f.searchErr
}

func (f *fakeJobProvider) CachedTorrents(_ context.Context, releases []acquisition.Release) ([]acquisition.Release, error) {
	f.cacheCalls++
	f.cacheInput = append([]acquisition.Release(nil), releases...)
	return append([]acquisition.Release(nil), f.cached...), f.cacheErr
}

func (f *fakeJobProvider) Materialize(context.Context, acquisition.Release) (acquisition.TorrentInput, error) {
	f.materializeCalls++
	return f.material, f.materializeErr
}

func (f *fakeJobProvider) FindTorrentByHash(context.Context, string) (acquisition.CreatedObject, error) {
	index := f.findCalls
	f.findCalls++
	var found acquisition.CreatedObject
	if index < len(f.foundSequence) {
		found = f.foundSequence[index]
	} else {
		found = f.found
	}
	if index < len(f.findErrs) {
		return found, f.findErrs[index]
	}
	return found, nil
}

func (f *fakeJobProvider) CreateTorrent(_ context.Context, _ acquisition.TorrentInput, allowDownload bool) (acquisition.CreatedObject, error) {
	f.createCalls++
	f.allowDownload = allowDownload
	return f.created, f.createErr
}

func (f *fakeJobProvider) InspectCreatedTorrent(context.Context, acquisition.CreatedObject) (acquisition.PreparationInspection, error) {
	index := f.inspectionCalls
	f.inspectionCalls++
	var inspectionErr error
	if index < len(f.inspectionErrs) {
		inspectionErr = f.inspectionErrs[index]
	}
	progress := 100
	if inspectionErr != nil {
		progress = 0
	}
	if index < len(f.inspectionProgress) {
		progress = f.inspectionProgress[index]
	} else if len(f.inspectionProgress) > 0 {
		progress = f.inspectionProgress[len(f.inspectionProgress)-1]
	}
	inspection, err := acquisition.NewPreparationInspection(f.candidates, progress)
	if err != nil {
		return acquisition.PreparationInspection{}, err
	}
	if inspectionErr != nil {
		return inspection, inspectionErr
	}
	return inspection, nil
}

func (f *fakeJobProvider) DeleteCreatedTorrent(_ context.Context, created acquisition.CreatedObject) error {
	f.deleteCalls++
	f.deleted = created
	return f.deleteErr
}

type fakeJobPublisher struct {
	published []acquisition.AcquiredMedia
	err       error
}

func (f *fakeJobPublisher) PublishAcquired(_ context.Context, media acquisition.AcquiredMedia) error {
	f.published = append(f.published, media)
	return f.err
}

func mustJobRelease(t *testing.T) acquisition.Release {
	t.Helper()
	return mustJobReleaseAt(t, 0)
}

func mustJobReleaseAt(t *testing.T, ordinal int) acquisition.Release {
	t.Helper()
	hashes := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"1123456789abcdef0123456789abcdef01234567",
		"2123456789abcdef0123456789abcdef01234567",
		"3123456789abcdef0123456789abcdef01234567",
		"4123456789abcdef0123456789abcdef01234567",
		"5123456789abcdef0123456789abcdef01234567",
	}
	require.GreaterOrEqual(t, ordinal, 0)
	require.Less(t, ordinal, len(hashes))
	hash := hashes[ordinal]
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "result-" + hash[:1], Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: int64(100 + ordinal), Indexer: "authorized-indexer",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
	})
	require.NoError(t, err)
	return release
}

func candidateHashes(candidates []acquisition.JobCandidate) []string {
	hashes := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		hashes = append(hashes, candidate.Selection().InfoHash())
	}
	return hashes
}

func mustJobSelection(t *testing.T) acquisition.JobSelection {
	t.Helper()
	selection, err := acquisition.NewJobSelection(mustJobRelease(t))
	require.NoError(t, err)
	return selection
}

func mustJobTorrentInput(t *testing.T) acquisition.TorrentInput {
	t.Helper()
	input, err := acquisition.NewMagnetTorrentInput(
		mustJobRelease(t).InfoHash(), mustJobRelease(t).MagnetURL(),
	)
	require.NoError(t, err)
	return input
}
