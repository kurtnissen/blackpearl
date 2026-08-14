// Package resolver coordinates provider-neutral acquisition discovery.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
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

// ReadySearchProvider is a configured primary search provider with a
// read-only readiness probe.
type ReadySearchProvider interface {
	SearchProvider
	Ready(ctx context.Context) error
}

// ReadySearcher preserves a primary provider's configuration probe while
// combining its searches with optional provider-neutral sources.
type ReadySearcher struct {
	primary  ReadySearchProvider
	searcher *Service
}

// PreferredSearcher returns a preferred provider's ranked results immediately
// and consults its fallback only when the preferred provider has no result.
type PreferredSearcher struct {
	preferred *Service
	fallback  *Service
}

// ReadyPreferredSearcher preserves fallback readiness for configuration while
// short-circuiting successful preferred searches.
type ReadyPreferredSearcher struct {
	primary  ReadySearchProvider
	searcher *PreferredSearcher
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

// NewReadySearcher constructs a readiness-preserving multi-provider searcher.
func NewReadySearcher(primary ReadySearchProvider, additional ...SearchProvider) (*ReadySearcher, error) {
	if primary == nil {
		return nil, errors.New("primary ready search provider is required")
	}
	providers := []SearchProvider{primary}
	providers = append(providers, additional...)
	for _, provider := range providers {
		if provider == nil {
			return nil, errors.New("ready search providers must not be nil")
		}
	}
	return &ReadySearcher{primary: primary, searcher: NewSearcher(providers...)}, nil
}

// NewPreferredSearcher constructs a two-stage provider-neutral searcher.
func NewPreferredSearcher(preferred SearchProvider, fallback SearchProvider) (*PreferredSearcher, error) {
	if preferred == nil || fallback == nil {
		return nil, errors.New("preferred and fallback search providers are required")
	}
	return &PreferredSearcher{preferred: NewSearcher(preferred), fallback: NewSearcher(fallback)}, nil
}

// NewReadyPreferredSearcher constructs a preferred searcher whose readiness
// remains owned by the configured credential-bearing fallback.
func NewReadyPreferredSearcher(primary ReadySearchProvider, preferred SearchProvider) (*ReadyPreferredSearcher, error) {
	if primary == nil {
		return nil, errors.New("primary ready search provider is required")
	}
	searcher, err := NewPreferredSearcher(preferred, primary)
	if err != nil {
		return nil, err
	}
	return &ReadyPreferredSearcher{primary: primary, searcher: searcher}, nil
}

// Name returns the configured primary provider name.
func (s *ReadySearcher) Name() string { return s.primary.Name() }

// Capabilities returns the configured primary provider capabilities.
func (s *ReadySearcher) Capabilities() acquisition.ProviderCapabilities {
	return s.primary.Capabilities()
}

// Ready probes only the configured credential-bearing primary provider.
func (s *ReadySearcher) Ready(ctx context.Context) error { return s.primary.Ready(ctx) }

// Search combines the primary and optional provider-neutral sources.
func (s *ReadySearcher) Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error) {
	return s.searcher.Search(ctx, request)
}

// Search returns preferred hits immediately and otherwise uses the fallback.
func (s *PreferredSearcher) Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error) {
	preferred, preferredErr := s.preferred.Search(ctx, request)
	if preferredErr == nil && len(preferred) > 0 {
		return preferred, nil
	}
	fallback, fallbackErr := s.fallback.Search(ctx, request)
	if fallbackErr == nil {
		return fallback, nil
	}
	if preferredErr != nil {
		return nil, errors.Join(preferredErr, fallbackErr)
	}
	return nil, fallbackErr
}

// Name returns the configured fallback provider name.
func (s *ReadyPreferredSearcher) Name() string { return s.primary.Name() }

// Capabilities returns the configured fallback provider capabilities.
func (s *ReadyPreferredSearcher) Capabilities() acquisition.ProviderCapabilities {
	return s.primary.Capabilities()
}

// Ready probes the configured credential-bearing fallback provider.
func (s *ReadyPreferredSearcher) Ready(ctx context.Context) error { return s.primary.Ready(ctx) }

// Search delegates to preferred-first search policy.
func (s *ReadyPreferredSearcher) Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error) {
	return s.searcher.Search(ctx, request)
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
