package acquisitionjob_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	acquisitionjobrepo "github.com/kurtnissen/blackpearl/internal/repository/acquisitionjob"
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

func TestRepositoryPlansAndReloadsBoundedReleaseCandidatesAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	repository, err := acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	candidates := mustCandidates(t, 3)

	require.NoError(t, repository.Plan(ctx, claim, candidates, at.Add(time.Second)))
	planned, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	ordinal, hasPlan := planned.SelectedCandidateOrdinal()
	require.True(t, hasPlan)
	require.Equal(t, 0, ordinal)
	require.Equal(t, candidates[0].Selection().InfoHash(), planned.Selection().InfoHash())
	actual, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, candidates, actual)

	require.NoError(t, repository.Close())
	repository, err = acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	reloaded, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, candidates, reloaded)
}

func TestRepositoryAdvancesCandidatesAndFailsAtomicallyWhenExhausted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	candidates := mustCandidates(t, 3)
	require.NoError(t, repository.Plan(ctx, claim, candidates, at.Add(time.Second)))

	selectedClaim, err := repository.Claim(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	require.NoError(t, repository.AttachPrepared(ctx, selectedClaim, created, true, at.Add(3*time.Second)))
	preparingClaim, err := repository.Claim(ctx, at.Add(4*time.Second), time.Minute)
	require.NoError(t, err)

	advanced, err := repository.Advance(
		ctx, preparingClaim, acquisition.CandidateOutcomeStalled, acquisition.JobErrorStalled, at.Add(5*time.Second),
	)
	require.NoError(t, err)
	require.True(t, advanced)
	next, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	ordinal, hasPlan := next.SelectedCandidateOrdinal()
	require.True(t, hasPlan)
	require.Equal(t, 1, ordinal)
	require.False(t, next.HasCreatedObject())
	require.False(t, next.CreatedByJob())
	require.Equal(t, candidates[1].Selection().InfoHash(), next.Selection().InfoHash())

	secondClaim, err := repository.Claim(ctx, at.Add(6*time.Second), time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.AttachPrepared(ctx, secondClaim, created, false, at.Add(7*time.Second)))
	secondPreparing, err := repository.Claim(ctx, at.Add(8*time.Second), time.Minute)
	require.NoError(t, err)
	advanced, err = repository.Advance(
		ctx, secondPreparing, acquisition.CandidateOutcomeMissing, acquisition.JobErrorStalled, at.Add(9*time.Second),
	)
	require.NoError(t, err)
	require.True(t, advanced)

	thirdClaim, err := repository.Claim(ctx, at.Add(10*time.Second), time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.AttachPrepared(ctx, thirdClaim, created, true, at.Add(11*time.Second)))
	thirdPreparing, err := repository.Claim(ctx, at.Add(12*time.Second), time.Minute)
	require.NoError(t, err)
	advanced, err = repository.Advance(
		ctx, thirdPreparing, acquisition.CandidateOutcomeUnplayable,
		acquisition.JobErrorNoPlayableMedia, at.Add(13*time.Second),
	)
	require.NoError(t, err)
	require.False(t, advanced)

	failed, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateFailed, failed.State())
	require.Equal(t, acquisition.JobErrorNoPlayableMedia, failed.ErrorCode())
	require.False(t, failed.HasSelection())
	require.False(t, failed.HasCreatedObject())
	_, hasPlan = failed.SelectedCandidateOrdinal()
	require.False(t, hasPlan)
	actual, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.CandidateOutcomeStalled, actual[0].Outcome())
	require.Equal(t, acquisition.CandidateOutcomeMissing, actual[1].Outcome())
	require.Equal(t, acquisition.CandidateOutcomeUnplayable, actual[2].Outcome())
}

func TestRepositoryAdvancesSelectedCandidateThatDisappearedBeforePreparation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	candidates := mustCandidates(t, 2)
	require.NoError(t, repository.Plan(ctx, claim, candidates, at.Add(time.Second)))
	selectedClaim, err := repository.Claim(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)

	advanced, err := repository.Advance(
		ctx, selectedClaim, acquisition.CandidateOutcomeMissing,
		acquisition.JobErrorMaterialization, at.Add(3*time.Second),
	)

	require.NoError(t, err)
	require.True(t, advanced)
	next, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, next.State())
	require.Equal(t, candidates[1].Selection().InfoHash(), next.Selection().InfoHash())
	actual, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.CandidateOutcomeMissing, actual[0].Outcome())
	require.Equal(t, acquisition.CandidateOutcomeSelected, actual[1].Outcome())
}

func TestRepositoryLegacySelectionDoesNotInferCandidatePlanOrOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openRepository(t, ctx)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Select(ctx, claim, mustSelection(t), at.Add(time.Second)))

	legacy, err := repository.Get(ctx, job.ID())

	require.NoError(t, err)
	_, hasPlan := legacy.SelectedCandidateOrdinal()
	require.False(t, hasPlan)
	require.False(t, legacy.CreatedByJob())
	candidates, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestRepositoryPersistsMixedTorrentAndRangePlanAcrossRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	repository, err := acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, _, err := repository.Submit(ctx, "0123456789abcdef0123456789abcdef", mustMovieRequest(t), at)
	require.NoError(t, err)
	claim, err := repository.Claim(ctx, at, time.Minute)
	require.NoError(t, err)

	torrent := mustCandidates(t, 1)[0].Selection()
	rangeSelection := mustRangeSelection(t)
	first, err := acquisition.NewJobCandidate(torrent, 0, acquisition.CandidateOutcomeSelected)
	require.NoError(t, err)
	second, err := acquisition.NewJobCandidate(rangeSelection, 1, acquisition.CandidateOutcomePending)
	require.NoError(t, err)
	require.NoError(t, repository.Plan(ctx, claim, []acquisition.JobCandidate{first, second}, at.Add(time.Second)))
	require.NoError(t, repository.Close())

	repository, err = acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	stored, err := repository.Candidates(ctx, job.ID())
	require.NoError(t, err)
	require.Len(t, stored, 2)
	require.Equal(t, acquisition.SelectionKindTorrent, stored[0].Selection().Kind())
	require.Equal(t, acquisition.SelectionKindRange, stored[1].Selection().Kind())
	require.Equal(t, rangeSelection.Identity(), stored[1].Selection().Identity())
	storedRange, ok := stored[1].Selection().RangeCandidate()
	require.True(t, ok)
	expectedRange, ok := rangeSelection.RangeCandidate()
	require.True(t, ok)
	require.Equal(t, expectedRange.Validator(), storedRange.Validator())

	selectedClaim, err := repository.Claim(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	advanced, err := repository.Advance(
		ctx,
		selectedClaim,
		acquisition.CandidateOutcomeMissing,
		acquisition.JobErrorNoPlayableMedia,
		at.Add(3*time.Second),
	)
	require.NoError(t, err)
	require.True(t, advanced)
	next, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.SelectionKindRange, next.Selection().Kind())
	require.Equal(t, rangeSelection.Identity(), next.Selection().Identity())
	nextRange, ok := next.Selection().RangeCandidate()
	require.True(t, ok)
	require.Equal(t, expectedRange.Validator(), nextRange.Validator())

	rangeClaim, err := repository.Claim(ctx, at.Add(4*time.Second), time.Minute)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("internet-archive-file", rangeSelection.Identity())
	require.NoError(t, err)
	require.NoError(t, repository.AttachPrepared(ctx, rangeClaim, created, false, at.Add(5*time.Second)))
	preparingClaim, err := repository.Claim(ctx, at.Add(6*time.Second), time.Minute)
	require.NoError(t, err)
	require.NoError(t, repository.Succeed(ctx, preparingClaim, rangeSelection.Identity(), at.Add(7*time.Second)))
	completed, err := repository.Get(ctx, job.ID())
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSucceeded, completed.State())
	require.Equal(t, acquisition.SelectionKindRange, completed.Selection().Kind())
	require.Equal(t, rangeSelection.Identity(), completed.PublishedObjectID())
}

func TestRepositoryMigrationPreservesVersionTwoTorrentJobsAndCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	createVersionTwoFixture(t, ctx, databasePath)

	repository, err := acquisitionjobrepo.Open(ctx, databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })

	queued, err := repository.Get(ctx, "00000000000000000000000000000000")
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateQueued, queued.State())
	require.False(t, queued.HasSelection())

	selected, err := repository.Get(ctx, "11111111111111111111111111111111")
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSelected, selected.State())
	require.Equal(t, acquisition.SelectionKindTorrent, selected.Selection().Kind())
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", selected.Selection().Identity())
	ordinal, planned := selected.SelectedCandidateOrdinal()
	require.True(t, planned)
	require.Zero(t, ordinal)
	candidates, err := repository.Candidates(ctx, selected.ID())
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	require.Equal(t, acquisition.CandidateOutcomeSelected, candidates[0].Outcome())
	require.Equal(t, acquisition.CandidateOutcomePending, candidates[1].Outcome())
	require.Equal(t, acquisition.SelectionKindTorrent, candidates[1].Selection().Kind())

	preparing, err := repository.Get(ctx, "22222222222222222222222222222222")
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStatePreparing, preparing.State())
	require.True(t, preparing.HasCreatedObject())
	require.Equal(t, "17", preparing.CreatedObject().ObjectID())

	succeeded, err := repository.Get(ctx, "33333333333333333333333333333333")
	require.NoError(t, err)
	require.Equal(t, acquisition.JobStateSucceeded, succeeded.State())
	require.Equal(t, "17:3", succeeded.PublishedObjectID())
	require.Equal(t, 100, succeeded.Progress())
}

func createVersionTwoFixture(t *testing.T, ctx context.Context, databasePath string) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
		CREATE TABLE acquisition_job_schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO acquisition_job_schema_migrations (name) VALUES ('001_jobs.sql'), ('002_candidate_fallback.sql');
		CREATE TABLE acquisition_jobs (
			id TEXT PRIMARY KEY, intent_key TEXT NOT NULL, media_type TEXT NOT NULL,
			title TEXT NOT NULL, release_year INTEGER NOT NULL, season INTEGER NOT NULL DEFAULT 0,
			episode INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL,
			selected_provider TEXT NOT NULL DEFAULT '', selected_title TEXT NOT NULL DEFAULT '',
			selected_size INTEGER NOT NULL DEFAULT 0, selected_indexer TEXT NOT NULL DEFAULT '',
			selected_info_hash TEXT NOT NULL DEFAULT '', selected_seeders INTEGER NOT NULL DEFAULT 0,
			selected_has_seeders INTEGER NOT NULL DEFAULT 0, created_provider TEXT NOT NULL DEFAULT '',
			created_object_id TEXT NOT NULL DEFAULT '', published_object_id TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '', attempt_count INTEGER NOT NULL DEFAULT 0,
			progress INTEGER NOT NULL DEFAULT 0, lease_version INTEGER NOT NULL DEFAULT 0,
			lease_until_unix_ms INTEGER NOT NULL DEFAULT 0, next_attempt_unix_ms INTEGER NOT NULL DEFAULT 0,
			created_unix_ms INTEGER NOT NULL, updated_unix_ms INTEGER NOT NULL,
			selected_candidate_ordinal INTEGER NOT NULL DEFAULT -1, created_by_job INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE acquisition_job_candidates (
			job_id TEXT NOT NULL REFERENCES acquisition_jobs(id) ON DELETE CASCADE,
			ordinal INTEGER NOT NULL, provider TEXT NOT NULL, title TEXT NOT NULL,
			size INTEGER NOT NULL, indexer TEXT NOT NULL, info_hash TEXT NOT NULL,
			seeders INTEGER NOT NULL DEFAULT 0, has_seeders INTEGER NOT NULL DEFAULT 0,
			outcome TEXT NOT NULL, PRIMARY KEY (job_id, ordinal), UNIQUE (job_id, info_hash)
		);
		CREATE UNIQUE INDEX acquisition_job_candidates_one_selected
			ON acquisition_job_candidates (job_id) WHERE outcome = 'selected';
	`)
	require.NoError(t, err)
	const insertJob = `
		INSERT INTO acquisition_jobs (
			id, intent_key, media_type, title, release_year, state,
			selected_provider, selected_title, selected_size, selected_indexer, selected_info_hash,
			created_provider, created_object_id, published_object_id, progress,
			created_unix_ms, updated_unix_ms, selected_candidate_ordinal
		) VALUES (?, ?, 'movie', ?, 2026, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	const hash = "0123456789abcdef0123456789abcdef01234567"
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC).UnixMilli()
	_, err = database.ExecContext(ctx, insertJob,
		"00000000000000000000000000000000", "queued", "Queued Movie", acquisition.JobStateQueued,
		"", "", 0, "", "", "", "", "", 0, at, at, -1,
	)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, insertJob,
		"11111111111111111111111111111111", "selected", "Selected Movie", acquisition.JobStateSelected,
		"prowlarr", "Selected.Movie.2026", 100, "authorized-indexer", hash, "", "", "", 0, at, at+1, 0,
	)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, insertJob,
		"22222222222222222222222222222222", "preparing", "Preparing Movie", acquisition.JobStatePreparing,
		"prowlarr", "Preparing.Movie.2026", 100, "authorized-indexer", hash, "torbox-torrent", "17", "", 42, at, at+2, -1,
	)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, insertJob,
		"33333333333333333333333333333333", "succeeded", "Succeeded Movie", acquisition.JobStateSucceeded,
		"prowlarr", "Succeeded.Movie.2026", 100, "authorized-indexer", hash, "torbox-torrent", "17", "17:3", 100, at, at+3, -1,
	)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
		INSERT INTO acquisition_job_candidates
			(job_id, ordinal, provider, title, size, indexer, info_hash, outcome)
		VALUES
			('11111111111111111111111111111111', 0, 'prowlarr', 'Selected.Movie.2026', 100, 'authorized-indexer', ?, 'selected'),
			('11111111111111111111111111111111', 1, 'prowlarr', 'Selected.Movie.2026.Alt', 101, 'authorized-indexer', ?, 'pending')
	`, hash, "1123456789abcdef0123456789abcdef01234567")
	require.NoError(t, err)
	require.NoError(t, database.Close())
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

func mustRangeSelection(t *testing.T) acquisition.JobSelection {
	t.Helper()
	media, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: "internet-archive-file", ObjectID: "opaque-object"},
		"Example.Movie.2026.mp4",
		175_099_607,
	)
	require.NoError(t, err)
	candidate, err := acquisition.NewRangeCandidate(media, "internet-archive", "sha1:fixture")
	require.NoError(t, err)
	selection, err := acquisition.NewRangeJobSelection(candidate)
	require.NoError(t, err)
	return selection
}

func mustCandidates(t *testing.T, count int) []acquisition.JobCandidate {
	t.Helper()
	result := make([]acquisition.JobCandidate, 0, count)
	for ordinal := range count {
		hash := []byte("0123456789abcdef0123456789abcdef01234567")
		hash[len(hash)-1] = "789abcdef"[ordinal]
		release, err := acquisition.NewRelease(acquisition.ReleaseInput{
			Provider: "prowlarr", SourceID: "private-result-url", Title: "Example.Movie.2026",
			Protocol: acquisition.ReleaseProtocolTorrent, Size: int64(100 + ordinal), Indexer: "authorized-indexer",
			InfoHash: string(hash), DownloadURL: "http://prowlarr:9696/private-proxy",
		})
		require.NoError(t, err)
		selection, err := acquisition.NewJobSelection(release)
		require.NoError(t, err)
		outcome := acquisition.CandidateOutcomePending
		if ordinal == 0 {
			outcome = acquisition.CandidateOutcomeSelected
		}
		candidate, err := acquisition.NewJobCandidate(selection, ordinal, outcome)
		require.NoError(t, err)
		result = append(result, candidate)
	}
	return result
}
