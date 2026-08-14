package watchlist_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	watchlistrepo "github.com/blackpearl-media/blackpearl/internal/repository/watchlist"
	"github.com/stretchr/testify/require"
)

func TestRepositoryPersistsSnapshotAndKeepsSucceededMovieFinal(t *testing.T) {
	t.Parallel()
	repository := openRepository(t, filepath.Join(t.TempDir(), "state", "blackpearl.db"))
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	movie := mustItem(t, "plex://movie/one", acquisitiondomain.WatchlistMediaTypeMovie, "Movie One")
	show := mustItem(t, "plex://show/one", acquisitiondomain.WatchlistMediaTypeShow, "Show One")

	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{movie, show}, now))
	status, err := repository.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, status.PendingMovies)
	require.Equal(t, 1, status.ObservedShows)

	claim, err := repository.Claim(context.Background(), now, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, movie, claim.Item())
	require.Equal(t, 1, claim.Attempt())
	_, err = repository.Claim(context.Background(), now, 5*time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	completion, err := acquisitiondomain.NewWatchlistSucceeded("torbox-object-17")
	require.NoError(t, err)
	require.NoError(t, repository.Complete(context.Background(), claim, completion))

	updatedMovie := mustItem(t, movie.ExternalID(), acquisitiondomain.WatchlistMediaTypeMovie, "Renamed Movie")
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{updatedMovie}, now.Add(time.Hour)))
	_, err = repository.Claim(context.Background(), now.Add(2*time.Hour), 5*time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	status, err = repository.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, status.Succeeded)
	require.Equal(t, 1, status.ObservedShows)
}

func TestRepositoryClaimsOnlyMoviesFirstObservedAfterAutomaticBaseline(t *testing.T) {
	t.Parallel()
	repository := openRepository(t, filepath.Join(t.TempDir(), "blackpearl.db"))
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	baseline := mustItem(t, "plex://movie/baseline", acquisitiondomain.WatchlistMediaTypeMovie, "Baseline")
	newItem := mustItem(t, "plex://movie/new", acquisitiondomain.WatchlistMediaTypeMovie, "New Item")
	require.NoError(t, repository.UpsertSnapshotPolicy(
		context.Background(), []acquisitiondomain.WatchlistItem{baseline}, now, false,
	))
	require.NoError(t, repository.UpsertSnapshotPolicy(
		context.Background(), []acquisitiondomain.WatchlistItem{baseline, newItem}, now.Add(time.Minute), true,
	))

	claim, err := repository.Claim(context.Background(), now.Add(time.Minute), time.Minute)

	require.NoError(t, err)
	require.Equal(t, newItem.ExternalID(), claim.Item().ExternalID())
	success, err := acquisitiondomain.NewWatchlistSucceeded("authorized-object")
	require.NoError(t, err)
	require.NoError(t, repository.Complete(context.Background(), claim, success))
	_, err = repository.Claim(context.Background(), now.Add(2*time.Minute), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestRepositoryDefersNotCachedMovieUntilCooldownExpires(t *testing.T) {
	t.Parallel()
	repository := openRepository(t, filepath.Join(t.TempDir(), "blackpearl.db"))
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/cooldown", acquisitiondomain.WatchlistMediaTypeMovie, "Cooldown"),
	}, now))
	claim, err := repository.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	completion, err := acquisitiondomain.NewWatchlistDeferred(acquisitiondomain.WatchlistQueueStateNotCached, now.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, repository.Complete(context.Background(), claim, completion))

	_, err = repository.Claim(context.Background(), now.Add(time.Hour-time.Millisecond), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	retry, err := repository.Claim(context.Background(), now.Add(time.Hour), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, retry.Attempt())
	require.Equal(t, int64(2), retry.LeaseVersion())
}

func TestRepositoryAttachesDurableJobAndRecoversItAfterRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blackpearl.db")
	repository, err := watchlistrepo.Open(context.Background(), path, true)
	require.NoError(t, err)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	reconcileAt := now.Add(time.Minute)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/job", acquisitiondomain.WatchlistMediaTypeMovie, "Durable Job"),
	}, now))
	claim, err := repository.Claim(context.Background(), now, 30*time.Second)
	require.NoError(t, err)
	jobID := "0123456789abcdef0123456789abcdef"

	require.NoError(t, repository.AttachJob(context.Background(), claim, jobID, reconcileAt))
	_, err = repository.Claim(context.Background(), reconcileAt.Add(-time.Millisecond), 30*time.Second)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, repository.Close())

	reopened := openRepository(t, path)
	recovered, err := reopened.Claim(context.Background(), reconcileAt, 30*time.Second)
	require.NoError(t, err)
	require.Equal(t, jobID, recovered.BackgroundJobID())
}

func TestRepositoryDefersLinkedJobAndRejectsStaleTransitions(t *testing.T) {
	t.Parallel()
	repository := openRepository(t, filepath.Join(t.TempDir(), "blackpearl.db"))
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/job-lease", acquisitiondomain.WatchlistMediaTypeMovie, "Job Lease"),
	}, now))
	first, err := repository.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	second, err := repository.Claim(context.Background(), now.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	jobID := "11111111111111111111111111111111"

	err = repository.AttachJob(context.Background(), first, jobID, now.Add(2*time.Minute))
	require.ErrorIs(t, err, acquisitiondomain.ErrStaleWatchlistClaim)
	require.NoError(t, repository.AttachJob(context.Background(), second, jobID, now.Add(2*time.Minute)))
	linked, err := repository.Claim(context.Background(), now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, jobID, linked.BackgroundJobID())

	staleLinked, claimErr := acquisitiondomain.NewWatchlistJobClaim(
		second.Item(), second.LeaseVersion(), second.Attempt(), jobID,
	)
	require.NoError(t, claimErr)
	err = repository.DeferJob(context.Background(), staleLinked, now.Add(3*time.Minute))
	require.ErrorIs(t, err, acquisitiondomain.ErrStaleWatchlistClaim)
	require.NoError(t, repository.DeferJob(context.Background(), linked, now.Add(3*time.Minute)))
	_, err = repository.Claim(context.Background(), now.Add(3*time.Minute-time.Millisecond), time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	retry, err := repository.Claim(context.Background(), now.Add(3*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, jobID, retry.BackgroundJobID())
}

func TestRepositoryReclaimsExpiredLeaseAndRejectsStaleCompletion(t *testing.T) {
	t.Parallel()
	repository := openRepository(t, filepath.Join(t.TempDir(), "blackpearl.db"))
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/lease", acquisitiondomain.WatchlistMediaTypeMovie, "Lease"),
	}, now))
	first, err := repository.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	second, err := repository.Claim(context.Background(), now.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, second.Attempt())
	require.Greater(t, second.LeaseVersion(), first.LeaseVersion())
	success, err := acquisitiondomain.NewWatchlistSucceeded("torbox-object-new")
	require.NoError(t, err)

	err = repository.Complete(context.Background(), first, success)
	require.ErrorIs(t, err, acquisitiondomain.ErrStaleWatchlistClaim)
	require.NoError(t, repository.Complete(context.Background(), second, success))
}

func TestRepositoryAllowsOnlyOneConcurrentClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blackpearl.db")
	repository := openRepository(t, path)
	secondRepository := openRepository(t, path)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/concurrent", acquisitiondomain.WatchlistMediaTypeMovie, "Concurrent"),
	}, now))

	const workers = 16
	results := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for index := range workers {
		candidate := repository
		if index%2 == 1 {
			candidate = secondRepository
		}
		go func(queue *watchlistrepo.Repository) {
			defer group.Done()
			_, err := queue.Claim(context.Background(), now, time.Minute)
			results <- err
		}(candidate)
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, domain.ErrNotFound)
	}
	require.Equal(t, 1, successes)
}

func TestRepositoryQueueSurvivesCloseAndReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blackpearl.db")
	repository, err := watchlistrepo.Open(context.Background(), path, true)
	require.NoError(t, err)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/persist", acquisitiondomain.WatchlistMediaTypeMovie, "Persist"),
	}, now))
	require.NoError(t, repository.Close())

	reopened, err := watchlistrepo.Open(context.Background(), path, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	claim, err := reopened.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "plex://movie/persist", claim.Item().ExternalID())
}

func TestRepositoryRejectsInvalidArgumentsAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	_, err := watchlistrepo.Open(context.Background(), "relative.db", true)
	require.Error(t, err)
	repository := openRepository(t, filepath.Join(t.TempDir(), "blackpearl.db"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = repository.UpsertSnapshot(ctx, nil, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	_, err = repository.Claim(ctx, time.Now(), time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	_, err = repository.Status(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = repository.Claim(context.Background(), time.Now(), 0)
	require.Error(t, err)
	err = repository.UpsertSnapshot(context.Background(), nil, time.Time{})
	require.Error(t, err)
	err = repository.Complete(context.Background(), acquisitiondomain.WatchlistClaim{}, acquisitiondomain.WatchlistCompletion{})
	require.Error(t, err)
}

func openRepository(t *testing.T, path string) *watchlistrepo.Repository {
	t.Helper()
	repository, err := watchlistrepo.Open(context.Background(), path, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	return repository
}

func TestRepositoryPersistsAcquisitionPolicyAndGatesClaimsAtomically(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blackpearl.db")
	repository, err := watchlistrepo.Open(context.Background(), path, false)
	require.NoError(t, err)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repository.UpsertSnapshot(context.Background(), []acquisitiondomain.WatchlistItem{
		mustItem(t, "plex://movie/policy", acquisitiondomain.WatchlistMediaTypeMovie, "Policy"),
	}, now))

	enabled, err := repository.AcquisitionEnabled(context.Background())
	require.NoError(t, err)
	require.False(t, enabled)
	_, err = repository.Claim(context.Background(), now, time.Minute)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, repository.SetAcquisitionEnabled(context.Background(), true))
	_, err = repository.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Close())

	reopened, err := watchlistrepo.Open(context.Background(), path, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	enabled, err = reopened.AcquisitionEnabled(context.Background())
	require.NoError(t, err)
	require.True(t, enabled)
}

func mustItem(t *testing.T, externalID string, mediaType acquisitiondomain.WatchlistMediaType, title string) acquisitiondomain.WatchlistItem {
	t.Helper()
	item, err := acquisitiondomain.NewWatchlistItem(acquisitiondomain.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: externalID, MediaType: mediaType, Title: title, Year: 2026,
	})
	require.NoError(t, err)
	return item
}
