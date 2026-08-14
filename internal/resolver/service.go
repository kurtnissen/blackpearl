// Package resolver coordinates provider-neutral acquisition discovery.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"golang.org/x/sync/errgroup"
)

// Provider is the narrow acquisition boundary consumed by the resolver.
type Provider interface {
	Name() string
	Resolve(ctx context.Context, request acquisition.Request) ([]acquisition.Candidate, error)
}

// SearchProvider is the narrow read-only acquisition search boundary consumed by the resolver.
type SearchProvider interface {
	Name() string
	Capabilities() acquisition.ProviderCapabilities
	Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error)
}

// Service combines normalized candidates from configured providers.
type Service struct {
	providers       []Provider
	searchProviders []SearchProvider
}

// New constructs a resolver from zero or more authorized providers.
func New(providers ...Provider) *Service {
	return &Service{providers: providers}
}

// NewSearcher constructs a resolver from zero or more authorized search providers.
func NewSearcher(providers ...SearchProvider) *Service {
	return &Service{searchProviders: append([]SearchProvider(nil), providers...)}
}

// Resolve returns provider-neutral candidates without applying acquisition policy.
func (s *Service) Resolve(ctx context.Context, request acquisition.Request) ([]acquisition.Candidate, error) {
	if len(s.providers) == 0 {
		return nil, fmt.Errorf("%w: no acquisition providers", domain.ErrNotConfigured)
	}
	var candidates []acquisition.Candidate
	for _, provider := range s.providers {
		resolved, err := provider.Resolve(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("resolve with provider %s: %w", provider.Name(), err)
		}
		candidates = append(candidates, resolved...)
	}
	return candidates, nil
}

// Search combines, deduplicates, and ranks read-only release results. A failed
// provider does not discard valid results from another provider.
func (s *Service) Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error) {
	if len(s.searchProviders) == 0 {
		return nil, fmt.Errorf("%w: no acquisition search providers", domain.ErrNotConfigured)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search acquisition providers: %w", err)
	}
	var mutex sync.Mutex
	var releases []acquisition.Release
	failures := make([]error, 0, len(s.searchProviders))
	successes := 0
	group, searchContext := errgroup.WithContext(ctx)
	for index, provider := range s.searchProviders {
		index := index
		provider := provider
		group.Go(func() error {
			found, err := provider.Search(searchContext, request)
			mutex.Lock()
			defer mutex.Unlock()
			if err != nil {
				failures = append(failures, sanitizedSearchError(provider.Name(), index, err))
				return nil
			}
			successes++
			releases = append(releases, found...)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, errors.New("wait for acquisition search providers")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search acquisition providers: %w", err)
	}
	if successes == 0 {
		return nil, errors.Join(failures...)
	}
	return rankAndDeduplicate(request, releases), nil
}

func sanitizedSearchError(providerName string, index int, providerErr error) error {
	name := providerName
	if _, err := domain.NewBackingRef(name, "search"); err != nil {
		name = fmt.Sprintf("provider-%d", index+1)
	}
	if errors.Is(providerErr, domain.ErrUnauthorized) {
		return fmt.Errorf("search provider %s rejected credentials: %w", name, domain.ErrUnauthorized)
	}
	return fmt.Errorf("search provider %s failed", name)
}
