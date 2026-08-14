package acquisition_test

import (
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestJobSelectionSupportsTorrentAndRangeVariants(t *testing.T) {
	t.Parallel()
	torrentRelease := mustCandidateSelection(t).Release()
	torrent, err := acquisition.NewTorrentJobSelection(torrentRelease)
	require.NoError(t, err)

	media, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: "internet-archive-file", ObjectID: "opaque-object"},
		"Example.Show.S01E01.mp4",
		175_099_607,
	)
	require.NoError(t, err)
	rangeCandidate, err := acquisition.NewRangeCandidate(media, "internet-archive", "sha1:fixture")
	require.NoError(t, err)
	require.Equal(t, "sha1:fixture", rangeCandidate.Validator())
	rangeSelection, err := acquisition.NewRangeJobSelection(rangeCandidate)
	require.NoError(t, err)

	require.Equal(t, acquisition.SelectionKindTorrent, torrent.Kind())
	require.Equal(t, torrentRelease.InfoHash(), torrent.Identity())
	reconstructedTorrent, ok := torrent.TorrentRelease()
	require.True(t, ok)
	require.Equal(t, torrentRelease.InfoHash(), reconstructedTorrent.InfoHash())
	_, ok = torrent.RangeCandidate()
	require.False(t, ok)

	require.Equal(t, acquisition.SelectionKindRange, rangeSelection.Kind())
	require.Equal(t, "opaque-object", rangeSelection.Identity())
	reconstructedRange, ok := rangeSelection.RangeCandidate()
	require.True(t, ok)
	require.Equal(t, media, reconstructedRange.Media())
	require.Equal(t, "internet-archive", reconstructedRange.Indexer())
	_, ok = rangeSelection.TorrentRelease()
	require.False(t, ok)
	require.Empty(t, rangeSelection.InfoHash())
	require.Equal(t, "internet-archive-file", rangeSelection.Provider())
	require.Equal(t, media.Name, rangeSelection.Title())
	require.Equal(t, media.Size, rangeSelection.Size())
	require.Equal(t, "internet-archive", rangeSelection.Indexer())
}

func TestRangeCandidateAndSelectionRejectInvalidVariantState(t *testing.T) {
	t.Parallel()
	validMedia, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: "internet-archive-file", ObjectID: "opaque-object"},
		"Example.Movie.2026.mp4",
		1024,
	)
	require.NoError(t, err)

	_, err = acquisition.NewRangeCandidate(domain.MediaCandidate{}, "internet-archive", "sha1:fixture")
	require.Error(t, err)
	_, err = acquisition.NewRangeCandidate(validMedia, "", "sha1:fixture")
	require.Error(t, err)
	_, err = acquisition.NewRangeCandidate(validMedia, "internet-archive", "")
	require.Error(t, err)
	_, err = acquisition.NewRangeJobSelection(acquisition.RangeCandidate{})
	require.Error(t, err)
	_, err = acquisition.NewTorrentJobSelection(acquisition.Release{})
	require.Error(t, err)
	_, err = acquisition.NewJobCandidate(acquisition.JobSelection{}, 0, acquisition.CandidateOutcomePending)
	require.Error(t, err)
}

func TestRangeJobSelectionSurvivesCandidateAndSnapshotValidation(t *testing.T) {
	t.Parallel()
	media, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: "internet-archive-file", ObjectID: "opaque-object"},
		"Example.Show.S01E01.mp4",
		2048,
	)
	require.NoError(t, err)
	rangeCandidate, err := acquisition.NewRangeCandidate(media, "internet-archive", "sha1:fixture")
	require.NoError(t, err)
	selection, err := acquisition.NewRangeJobSelection(rangeCandidate)
	require.NoError(t, err)
	candidate, err := acquisition.NewJobCandidate(selection, 0, acquisition.CandidateOutcomeSelected)
	require.NoError(t, err)
	require.Equal(t, acquisition.SelectionKindRange, candidate.Selection().Kind())

	request, err := acquisition.NewEpisodeSearch("Example Show", 2026, 1, 1)
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	ordinal := 0
	snapshot, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateSelected, Selection: &selection,
		SelectedCandidateOrdinal: &ordinal, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	require.Equal(t, acquisition.SelectionKindRange, snapshot.Selection().Kind())
	require.Equal(t, "opaque-object", snapshot.Selection().Identity())
}

func TestNewAcquisitionJobSnapshotEnforcesDurableStageInvariants(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "result-1", Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 100, Indexer: "authorized-indexer",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	selection, err := acquisition.NewJobSelection(release)
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	createdAt := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)

	queued, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateQueued, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
	require.NoError(t, err)
	require.Equal(t, request, queued.Request())
	require.False(t, queued.HasSelection())

	selected, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateSelected, Selection: &selection,
		Attempt: 1, CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Second),
	})
	require.NoError(t, err)
	require.True(t, selected.HasSelection())
	require.Equal(t, release.InfoHash(), selected.Selection().InfoHash())

	preparing, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStatePreparing, Selection: &selection, CreatedObject: &created,
		Attempt: 2, Progress: 42, CreatedAt: createdAt, UpdatedAt: createdAt.Add(2 * time.Second),
	})
	require.NoError(t, err)
	require.True(t, preparing.HasCreatedObject())
	require.Equal(t, 42, preparing.Progress())

	succeeded, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateSucceeded, Selection: &selection, CreatedObject: &created,
		PublishedObjectID: "17:3", Attempt: 3, Progress: 100,
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(3 * time.Second),
	})
	require.NoError(t, err)
	require.Equal(t, "17:3", succeeded.PublishedObjectID())

	for _, input := range []acquisition.JobSnapshotInput{
		{ID: "not-hex", Request: request, State: acquisition.JobStateQueued, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: "0123456789abcdef0123456789abcdef", Request: request, State: acquisition.JobStateSelected, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: "0123456789abcdef0123456789abcdef", Request: request, State: acquisition.JobStatePreparing, Selection: &selection, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: "0123456789abcdef0123456789abcdef", Request: request, State: acquisition.JobStateSucceeded, Selection: &selection, CreatedObject: &created, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: "0123456789abcdef0123456789abcdef", Request: request, State: acquisition.JobStateQueued, Progress: 101, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: "0123456789abcdef0123456789abcdef", Request: request, State: acquisition.JobStateQueued, CreatedAt: createdAt, UpdatedAt: createdAt.Add(-time.Second)},
	} {
		_, snapshotErr := acquisition.NewAcquisitionJobSnapshot(input)
		require.Error(t, snapshotErr)
	}
}

func TestNewJobSelectionStripsEphemeralLocatorsAndReconstructsSafeRelease(t *testing.T) {
	t.Parallel()
	seeders := 5
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "https://private.example/result/1", Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 100, Indexer: "authorized-indexer",
		InfoHash:    "0123456789abcdef0123456789abcdef01234567",
		MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		DownloadURL: "http://prowlarr:9696/api/v1/indexer/1/download?apikey=private",
		Seeders:     &seeders,
	})
	require.NoError(t, err)

	selection, err := acquisition.NewJobSelection(release)

	require.NoError(t, err)
	require.Equal(t, release.InfoHash(), selection.InfoHash())
	reconstructed := selection.Release()
	require.Equal(t, release.Provider(), reconstructed.Provider())
	require.Equal(t, release.Title(), reconstructed.Title())
	require.Equal(t, release.Indexer(), reconstructed.Indexer())
	require.Empty(t, reconstructed.MagnetURL())
	require.Empty(t, reconstructed.DownloadURL())
	require.NotContains(t, reconstructed.SourceID(), "private")

	usenet, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "nzb", Title: "Example", Protocol: acquisition.ReleaseProtocolUsenet,
		Size: 100, Indexer: "authorized-indexer", DownloadURL: "http://prowlarr:9696/nzb",
	})
	require.NoError(t, err)
	_, err = acquisition.NewJobSelection(usenet)
	require.Error(t, err)
}

func TestNewAcquisitionJobClaimRequiresPositiveLeaseVersion(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateQueued, Attempt: 1, CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)

	claim, err := acquisition.NewAcquisitionJobClaim(job, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), claim.LeaseVersion())
	require.Equal(t, job.ID(), claim.Job().ID())

	_, err = acquisition.NewAcquisitionJobClaim(job, 0)
	require.Error(t, err)
	_, err = acquisition.NewAcquisitionJobClaim(acquisition.AcquisitionJob{}, 1)
	require.Error(t, err)

	_, err = acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateFailed, ErrorCode: acquisition.JobErrorNoRelease,
		CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateManualReview, ErrorCode: acquisition.JobErrorAmbiguousMutation,
		CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, err = acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateFailed, ErrorCode: acquisition.JobErrorCode("private provider detail"),
		CreatedAt: at, UpdatedAt: at,
	})
	require.Error(t, err)

}

func TestNewJobCandidateValidatesBoundedDurableReleaseOutcome(t *testing.T) {
	t.Parallel()
	selection := mustCandidateSelection(t)

	candidate, err := acquisition.NewJobCandidate(selection, 0, acquisition.CandidateOutcomeSelected)

	require.NoError(t, err)
	require.Equal(t, 0, candidate.Ordinal())
	require.Equal(t, selection.InfoHash(), candidate.Selection().InfoHash())
	require.Equal(t, acquisition.CandidateOutcomeSelected, candidate.Outcome())

	for _, input := range []struct {
		ordinal int
		outcome acquisition.CandidateOutcome
	}{
		{ordinal: -1, outcome: acquisition.CandidateOutcomePending},
		{ordinal: 5, outcome: acquisition.CandidateOutcomePending},
		{ordinal: 1, outcome: acquisition.CandidateOutcome("private provider error")},
	} {
		_, candidateErr := acquisition.NewJobCandidate(selection, input.ordinal, input.outcome)
		require.Error(t, candidateErr)
	}
}

func TestAcquisitionJobCandidateProvenanceRequiresCompatibleStage(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	selection := mustCandidateSelection(t)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	ordinal := 0

	preparing, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStatePreparing, Selection: &selection, CreatedObject: &created,
		SelectedCandidateOrdinal: &ordinal, CreatedByJob: true,
		CreatedAt: at, UpdatedAt: at,
	})

	require.NoError(t, err)
	actualOrdinal, planned := preparing.SelectedCandidateOrdinal()
	require.True(t, planned)
	require.Equal(t, 0, actualOrdinal)
	require.True(t, preparing.CreatedByJob())

	for _, input := range []acquisition.JobSnapshotInput{
		{
			ID: "0123456789abcdef0123456789abcdef", Request: request,
			State: acquisition.JobStateQueued, SelectedCandidateOrdinal: &ordinal,
			CreatedAt: at, UpdatedAt: at,
		},
		{
			ID: "0123456789abcdef0123456789abcdef", Request: request,
			State: acquisition.JobStateSelected, Selection: &selection,
			SelectedCandidateOrdinal: &ordinal, CreatedByJob: true,
			CreatedAt: at, UpdatedAt: at,
		},
		{
			ID: "0123456789abcdef0123456789abcdef", Request: request,
			State: acquisition.JobStatePreparing, Selection: &selection,
			SelectedCandidateOrdinal: &ordinal, CreatedByJob: true,
			CreatedAt: at, UpdatedAt: at,
		},
	} {
		_, snapshotErr := acquisition.NewAcquisitionJobSnapshot(input)
		require.Error(t, snapshotErr)
	}

	legacy, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request,
		State: acquisition.JobStateSelected, Selection: &selection,
		CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	_, planned = legacy.SelectedCandidateOrdinal()
	require.False(t, planned)
	require.False(t, legacy.CreatedByJob())
}

func mustCandidateSelection(t *testing.T) acquisition.JobSelection {
	t.Helper()
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "result-1", Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 100, Indexer: "authorized-indexer",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	selection, err := acquisition.NewJobSelection(release)
	require.NoError(t, err)
	return selection
}
