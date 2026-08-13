package resolver_test

import (
	"context"
	"errors"
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

type fakeProvider struct {
	name       string
	candidates []acquisition.Candidate
	err        error
	received   acquisition.Request
}

func (f *fakeProvider) Name() string {
	return f.name
}

func (f *fakeProvider) Resolve(_ context.Context, request acquisition.Request) ([]acquisition.Candidate, error) {
	f.received = request
	return f.candidates, f.err
}
