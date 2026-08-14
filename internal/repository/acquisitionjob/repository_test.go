package acquisitionjob_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionjobrepo "github.com/blackpearl-media/blackpearl/internal/repository/acquisitionjob"
	"github.com/stretchr/testify/require"
)

func TestRepositorySubmitDeduplicatesOnlyActiveIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	request := mustMovieRequest(t)

	first, created, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", request, at)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, acquisition.JobStateQueued, first.State())

	duplicate, created, err := repository.Submit(ctx, "11111111111111111111111111111111", request, at.Add(time.Second))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID(), duplicate.ID())

	claim, err := repository.Claim(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Fail(ctx, claim, acquisition.JobErrorNoRelease, false, at.Add(3*time.Second)))

	replacement, created, err := repository.Submit(ctx, "22222222222222222222222222222222", request, at.Add(4*time.Second))
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.ID(), replacement.ID())
}

func TestRepositoryLeasesPersistSelectionBeforeAttachAndRejectStaleClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	repository, err := acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)

	firstClaim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, firstClaim.Job().Attempt())
	_, err = repository.Claim(ctx, at.Add(30*time.Second), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)

	reclaimed, err := repository.Claim(ctx, at.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, reclaimed.Job().Attempt())
	require.Greater(t, reclaimed.LeaseVersion(), firstClaim.LeaseVersion())

	selection := mustSelection(t)
	err = repository.Select(ctx, firstClaim, selection, at.Add(time.Minute+time.Second))
	require.ErrorIs(t, err, acquisitionjobrepo.ErrStaleClaim)
	require.NoError(t, repository.Select(ctx, reclaimed, selection, at.Add(time.Minute+time.Second)))

	selected, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, selected.State())
	require.Equal(t, selection.InfoHash(), selected.Selection().InfoHash())
	require.False(t, selected.HasCreatedObject())

	require.NoError(t, repository.Close())
	repository, err = acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	attachClaim, err := repository.Claim(ctx, at.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	require.NoError(t, repository.Attach(ctx, attachClaim, created, at.Add(2*time.Minute+time.Second)))

	preparing, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, preparing.State())
	require.Equal(t, "17", preparing.CreatedObject().ObjectID())
}

func TestRepositoryDefersAndCompletesPreparingJobWithVersionedTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	_, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	queuedClaim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Select(ctx, queuedClaim, mustSelection(t), at.Add(time.Second)))
	selectedClaim, err := repository.Claim(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	require.NoError(t, repository.Attach(ctx, selectedClaim, created, at.Add(3*time.Second)))
	preparingClaim, err := repository.Claim(ctx, at.Add(4*time.Second), time.Minute)
	require.NoError(t, err)

	next := at.Add(10 * time.Minute)
	require.NoError(t, repository.Defer(ctx, preparingClaim, next, acquisition.JobErrorProviderUnavailable, 37, at.Add(5*time.Second)))
	_, err = repository.Claim(ctx, next.Add(-time.Second), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	resumed, err := repository.Claim(ctx, next, time.Minute)
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, resumed.Job().State())
	require.Equal(t, 37, resumed.Job().Progress())
	require.Equal(t, acquisition.JobErrorProviderUnavailable, resumed.Job().ErrorCode())

	require.NoError(t, repository.Succeed(ctx, resumed, "17:3", next.Add(time.Second)))
	completed, err := repository.Get(ctx, resumed.Job().ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSucceeded, completed.State())
	require.Equal(t, 100, completed.Progress())
	require.Equal(t, "17:3", completed.PublishedObjectID())

	_, err = repository.Claim(ctx, next.Add(2*time.Second), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	jobs, err := repository.List(ctx, 20)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, completed.ID(), jobs[0].ID())
}

func TestRepositoryRejectsInvalidInputsAndDoesNotPersistPrivateErrorText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	request := mustMovieRequest(t)

	_, _, err := repository.Submit(ctx, "bad", request, at)
	require.Error(t, err)
	_, _, err = repository.Submit(ctx, "0123456789abcdef0123456789abcdef", request, time.Time{})
	require.Error(t, err)
	_, err = repository.Get(ctx, "../private")
	require.Error(t, err)
	_, err = repository.List(ctx, 0)
	require.Error(t, err)

	_, _, err = repository.Submit(ctx, "0123456789abcdef0123456789abcdef", request, at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	err = repository.Fail(ctx, claim, acquisition.JobErrorCode("private-token-detail"), false, at.Add(time.Second))
	require.Error(t, err)

	job, err := repository.Get(ctx, claim.Job().ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateQueued, job.State())
	require.Empty(t, job.ErrorCode())

	require.ErrorIs(t, repository.Fail(ctx, claim, acquisition.JobErrorAmbiguousMutation, true, at.Add(time.Second)), nil)
	manual, err := repository.Get(ctx, claim.Job().ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateManualReview, manual.State())
	_, err = acquisition.NewAcquisitionJobClaim(manual, 3)
	require.Error(t, err)
}

func openRepository(t *testing.T, ctx context.Context) *acquisitionjobrepo.Repository {
	t.Helper()
	repository, err := acquisitionjobrepo.Open(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	return repository
}

func mustMovieRequest(t *testing.T) acquisition.SearchRequest {
	t.Helper()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	return request
}

func mustSelection(t *testing.T) acquisition.JobSelection {
	t.Helper()
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "private-result-url", Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 100, Indexer: "authorized-indexer",
		InfoHash:    "0123456789abcdef0123456789abcdef01234567",
		DownloadURL: "http://prowlarr:9696/private-proxy",
	})
	require.NoError(t, err)
	selection, err := acquisition.NewJobSelection(release)
	require.NoError(t, err)
	return selection
}
