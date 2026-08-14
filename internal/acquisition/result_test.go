package acquisition_test

import (
	"fmt"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewCreatedObjectValidatesAndCopiesProviderReference(t *testing.T) {
	t.Parallel()

	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")

	require.NoError(t, err)
	require.Equal(t, "torbox-torrent", created.Provider())
	require.Equal(t, "17", created.ObjectID())
	require.Equal(t, domain.BackingRef{Provider: "torbox-torrent", ObjectID: "17"}, created.Backing())
}

func TestNewCreatedObjectRejectsInvalidReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		objectID string
	}{
		{name: "missing provider", objectID: "17"},
		{name: "unsafe provider", provider: "TorBox", objectID: "17"},
		{name: "missing object", provider: "torbox-torrent"},
		{name: "nul object", provider: "torbox-torrent", objectID: "17\x00bad"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := acquisition.NewCreatedObject(test.provider, test.objectID)

			require.Error(t, err)
		})
	}
}

func TestNewPreparationInspectionValidatesProgressAndDefensivelyCopiesCandidates(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:3", "Example.2026.mp4", 10)
	require.NoError(t, err)
	candidates := []domain.MediaCandidate{candidate}

	inspection, err := acquisition.NewPreparationInspection(candidates, 42)

	require.NoError(t, err)
	require.Equal(t, 42, inspection.Progress())
	candidates[0].Name = "mutated.mp4"
	first := inspection.Candidates()
	require.Equal(t, candidate, first[0])
	first[0].Name = "also-mutated.mp4"
	require.Equal(t, candidate, inspection.Candidates()[0])
}

func TestNewPreparationInspectionAcceptsBoundaryProgress(t *testing.T) {
	t.Parallel()

	for _, progress := range []int{0, 100} {
		t.Run(fmt.Sprintf("progress %d", progress), func(t *testing.T) {
			t.Parallel()

			inspection, err := acquisition.NewPreparationInspection(nil, progress)

			require.NoError(t, err)
			require.Equal(t, progress, inspection.Progress())
			require.Empty(t, inspection.Candidates())
		})
	}
}

func TestNewPreparationInspectionRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	valid, err := domain.NewMediaCandidate("17:3", "Example.2026.mp4", 10)
	require.NoError(t, err)
	invalidExtension := valid
	invalidExtension.Extension = ".mkv"

	tests := []struct {
		name       string
		candidates []domain.MediaCandidate
		progress   int
	}{
		{name: "negative progress", progress: -1},
		{name: "progress over one hundred", progress: 101},
		{name: "zero candidate", candidates: []domain.MediaCandidate{{}}, progress: 1},
		{name: "mismatched extension", candidates: []domain.MediaCandidate{invalidExtension}, progress: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, inspectionErr := acquisition.NewPreparationInspection(test.candidates, test.progress)

			require.Error(t, inspectionErr)
		})
	}
}

func TestNewAcquiredMediaPreservesValidatedIntentReleaseAndCandidate(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Example Show", 2026, 7, 2)
	require.NoError(t, err)
	seeders := 42
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "release-1", Title: "Example.Show.S07E02.1080p",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 20, Indexer: "authorized-indexer",
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Seeders: &seeders,
	})
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.Show.S07E02.mkv", 10)
	require.NoError(t, err)

	result, err := acquisition.NewAcquiredMedia(request, release, candidate)

	require.NoError(t, err)
	require.Equal(t, request, result.Request())
	require.Equal(t, release, result.Release())
	require.Equal(t, candidate, result.Candidate())
}

func TestNewRangeAcquiredMediaPreservesValidatedIntentAndProviderCandidate(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Example Show", 2026, 1, 1)
	require.NoError(t, err)
	backing, err := domain.NewBackingRef("internet-archive-file", "opaque")
	require.NoError(t, err)
	candidate, err := domain.NewProviderMediaCandidate(backing, "Example.Show.S01E01.mp4", 1024)
	require.NoError(t, err)

	result, err := acquisition.NewRangeAcquiredMedia(request, candidate)

	require.NoError(t, err)
	require.Equal(t, request, result.Request())
	require.Equal(t, candidate, result.Candidate())
	require.Equal(t, backing, result.Candidate().Backing())
}

func TestNewAcquiredMediaRejectsZeroOrMismatchedValues(t *testing.T) {
	t.Parallel()
	movie, err := acquisition.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	torrent, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "release-1", Title: "Example.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 20, Indexer: "authorized-indexer",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	usenet, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "release-2", Title: "Example.2026",
		Protocol: acquisition.ReleaseProtocolUsenet, Size: 20, Indexer: "authorized-indexer",
		DownloadURL: "https://indexer.invalid/release.nzb",
	})
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.2026.mkv", 10)
	require.NoError(t, err)

	tests := []struct {
		name      string
		request   acquisition.SearchRequest
		release   acquisition.Release
		candidate domain.MediaCandidate
	}{
		{name: "zero request", release: torrent, candidate: candidate},
		{name: "zero release", request: movie, candidate: candidate},
		{name: "usenet release", request: movie, release: usenet, candidate: candidate},
		{name: "zero candidate", request: movie, release: torrent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, resultErr := acquisition.NewAcquiredMedia(test.request, test.release, test.candidate)

			require.Error(t, resultErr)
		})
	}
}
