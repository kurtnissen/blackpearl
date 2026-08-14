package resolver_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/resolver"
	"github.com/stretchr/testify/require"
)

func TestResolveReturnsNotConfiguredWithoutProviders(t *testing.T) {
	t.Parallel()
	service := resolver.New()

	_, err := service.Resolve(context.Background(), acquisition.Request{MediaID: "id"})

	require.ErrorIs(t, err, domain.ErrNotConfigured)
}

func TestResolveCombinesProviderNeutralCandidates(t *testing.T) {
	t.Parallel()
	request := acquisition.Request{MediaID: "id", VirtualPath: "Movies/Movie/Movie.mp4"}
	first := &fakeProvider{
		name: "first",
		candidates: []acquisition.Candidate{{
			Backing: domain.BackingRef{Provider: "first", ObjectID: "a"},
			Size:    10,
		}},
	}
	second := &fakeProvider{
		name: "second",
		candidates: []acquisition.Candidate{{
			Backing: domain.BackingRef{Provider: "second", ObjectID: "b"},
			Size:    11,
		}},
	}
	service := resolver.New(first, second)

	actual, err := service.Resolve(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, append(first.candidates, second.candidates...), actual)
	require.Equal(t, request, first.received)
	require.Equal(t, request, second.received)
}

func TestResolveWrapsProviderFailure(t *testing.T) {
	t.Parallel()
	service := resolver.New(&fakeProvider{name: "broken", err: errors.New("offline")})

	_, err := service.Resolve(context.Background(), acquisition.Request{MediaID: "id"})

	require.ErrorContains(t, err, "provider broken")
}

func TestSearchReturnsNotConfiguredWithoutProviders(t *testing.T) {
	t.Parallel()
	service := resolver.NewSearcher()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)

	_, err = service.Search(context.Background(), request)

	require.ErrorIs(t, err, domain.ErrNotConfigured)
}

func TestSearchReturnsRankedResultsWhenAnotherProviderFails(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Otherhood", 2019)
	require.NoError(t, err)
	release := mustSearchRelease(t, acquisition.ReleaseInput{
		Provider: "working", SourceID: "one", Title: "Otherhood.2019.1080p",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1000, Indexer: "indexer",
		InfoHash: "abcdef0123456789abcdef0123456789abcdef01",
	})
	working := &fakeSearchProvider{name: "working", releases: []acquisition.Release{release}}
	broken := &fakeSearchProvider{name: "broken", err: errors.New("provider failed at https://secret.test/download")}
	service := resolver.NewSearcher(broken, working)

	actual, err := service.Search(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, []acquisition.Release{release}, actual)
	require.Equal(t, request, working.received)
	require.Equal(t, request, broken.received)
}

func TestSearchReturnsEmptySuccessWhenProviderHasNoResults(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	service := resolver.NewSearcher(
		&fakeSearchProvider{name: "empty"},
		&fakeSearchProvider{name: "broken", err: errors.New("offline")},
	)

	releases, err := service.Search(context.Background(), request)

	require.NoError(t, err)
	require.Empty(t, releases)
}

func TestSearchJoinsSanitizedErrorsWhenEveryProviderFails(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	service := resolver.NewSearcher(
		&fakeSearchProvider{name: "first", err: fmt.Errorf("API key secret: %w", domain.ErrUnauthorized)},
		&fakeSearchProvider{name: "https://provider-name-secret.test", err: errors.New("https://signed.test/download?secret=value")},
	)

	_, err = service.Search(context.Background(), request)

	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	require.ErrorContains(t, err, "first")
	require.ErrorContains(t, err, "provider-2")
	require.NotContains(t, err.Error(), "API key secret")
	require.NotContains(t, err.Error(), "signed.test")
	require.NotContains(t, err.Error(), "provider-name-secret")
}

func TestSearchReturnsCancellation(t *testing.T) {
	t.Parallel()
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	provider := &fakeSearchProvider{name: "waiting", search: func(ctx context.Context, _ acquisition.SearchRequest) ([]acquisition.Release, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := resolver.NewSearcher(provider)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = service.Search(ctx, request)

	require.ErrorIs(t, err, context.Canceled)
}

type fakeProvider struct {
	name       string
	candidates []acquisition.Candidate
	err        error
	received   acquisition.Request
}

type fakeSearchProvider struct {
	name     string
	releases []acquisition.Release
	err      error
	received acquisition.SearchRequest
	search   func(context.Context, acquisition.SearchRequest) ([]acquisition.Release, error)
}

func (f *fakeSearchProvider) Name() string { return f.name }

func (f *fakeSearchProvider) Capabilities() acquisition.ProviderCapabilities {
	return acquisition.NewProviderCapabilities(
		[]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent}, true, true, true,
	)
}

func (f *fakeSearchProvider) Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error) {
	f.received = request
	if f.search != nil {
		return f.search(ctx, request)
	}
	return f.releases, f.err
}

func mustSearchRelease(t *testing.T, input acquisition.ReleaseInput) acquisition.Release {
	t.Helper()
	release, err := acquisition.NewRelease(input)
	require.NoError(t, err)
	return release
}

func (f *fakeProvider) Name() string {
	return f.name
}

func (f *fakeProvider) Resolve(_ context.Context, request acquisition.Request) ([]acquisition.Candidate, error) {
	f.received = request
	return f.candidates, f.err
}
