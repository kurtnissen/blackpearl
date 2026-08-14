package directrange_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/service/directrange"
	"github.com/stretchr/testify/require"
)

func TestResolverReturnsOneExactPlayableFilePerEligibleRelease(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	exact := directRelease(t, "exact", "Example Movie (2026)")
	trailer := directRelease(t, "trailer", "Example Movie Trailer (2026)")
	gateway := &fakeDirectGateway{
		releases: []acquisition.Release{trailer, exact},
		files: map[string][]acquisition.RangeCandidate{
			"exact": {
				directCandidate(t, "small", "Example.Movie.2026.mp4", 10),
				directCandidate(t, "large", "Example.Movie.2026.mkv", 20),
			},
			"trailer": {directCandidate(t, "trailer-file", "Example.Movie.Trailer.2026.mp4", 100)},
		},
	}
	resolver, err := directrange.NewResolver(gateway)
	require.NoError(t, err)

	resolved, err := resolver.Resolve(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, "large", resolved[0].Media().ObjectID)
	require.Equal(t, []string{"exact"}, gateway.listed)
}

func TestResolverSelectsExactEpisodeAndRejectsAuxiliaryFiles(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewEpisodeSearch("Example Show", 2026, 1, 1)
	require.NoError(t, err)
	release := directRelease(t, "episode", "Example Show S01E01")
	gateway := &fakeDirectGateway{
		releases: []acquisition.Release{release},
		files: map[string][]acquisition.RangeCandidate{
			"episode": {
				directCandidate(t, "sample", "Example.Show.S01E01.sample.mp4", 100),
				directCandidate(t, "episode-file", "Example.Show.S01E01.mp4", 20),
			},
		},
	}
	resolver, err := directrange.NewResolver(gateway)
	require.NoError(t, err)

	resolved, err := resolver.Resolve(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, "episode-file", resolved[0].Media().ObjectID)
}

func TestResolverToleratesItemFailureButRejectsTotalProviderFailure(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	first := directRelease(t, "first", "Example Movie (2026)")
	second := directRelease(t, "second", "Example Movie (2026)")
	gateway := &fakeDirectGateway{
		releases: []acquisition.Release{first, second},
		files: map[string][]acquisition.RangeCandidate{
			"second": {directCandidate(t, "working", "Example.Movie.2026.mp4", 20)},
		},
		failures: map[string]error{"first": errors.New("private URL detail")},
	}
	resolver, err := directrange.NewResolver(gateway)
	require.NoError(t, err)

	resolved, err := resolver.Resolve(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	gateway.failures["second"] = errors.New("other private detail")
	_, err = resolver.Resolve(context.Background(), request)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private")
}

func TestResolverHonorsCancellationAndBoundsResults(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	gateway := &fakeDirectGateway{}
	for index := 0; index < acquisition.MaximumJobCandidates+2; index++ {
		id := string(rune('a' + index))
		release := directRelease(t, id, "Example Movie (2026)")
		gateway.releases = append(gateway.releases, release)
		if gateway.files == nil {
			gateway.files = make(map[string][]acquisition.RangeCandidate)
		}
		gateway.files[id] = []acquisition.RangeCandidate{directCandidate(t, "object-"+id, "Example.Movie.2026.mp4", int64(10+index))}
	}
	resolver, err := directrange.NewResolver(gateway)
	require.NoError(t, err)

	resolved, err := resolver.Resolve(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, resolved, acquisition.MaximumJobCandidates)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = resolver.Resolve(canceled, request)
	require.ErrorIs(t, err, context.Canceled)
}

type fakeDirectGateway struct {
	releases []acquisition.Release
	files    map[string][]acquisition.RangeCandidate
	failures map[string]error
	listed   []string
}

func (g *fakeDirectGateway) Search(ctx context.Context, _ acquisition.SearchRequest) ([]acquisition.Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]acquisition.Release(nil), g.releases...), nil
}

func (g *fakeDirectGateway) ListRangeCandidates(ctx context.Context, release acquisition.Release) ([]acquisition.RangeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.listed = append(g.listed, release.SourceID())
	if err := g.failures[release.SourceID()]; err != nil {
		return nil, err
	}
	return append([]acquisition.RangeCandidate(nil), g.files[release.SourceID()]...), nil
}

func directRelease(t *testing.T, sourceID string, title string) acquisition.Release {
	t.Helper()
	hash := "0123456789abcdef0123456789abcdef01234567"
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: sourceID, Title: title,
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 100, Indexer: "Internet Archive", InfoHash: hash,
	})
	require.NoError(t, err)
	return release
}

func directCandidate(t *testing.T, objectID string, name string, size int64) acquisition.RangeCandidate {
	t.Helper()
	media, err := domain.NewProviderMediaCandidate(
		domain.BackingRef{Provider: "internet-archive-file", ObjectID: objectID}, name, size,
	)
	require.NoError(t, err)
	candidate, err := acquisition.NewRangeCandidate(media, "Internet Archive", "sha1:fixture-"+objectID)
	require.NoError(t, err)
	return candidate
}
