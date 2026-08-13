// Package resolver coordinates provider-neutral acquisition discovery.
package resolver

import (
	"context"
	"fmt"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// Provider is the narrow acquisition boundary consumed by the resolver.
type Provider interface {
	Name() string
	Resolve(ctx context.Context, request acquisition.Request) ([]acquisition.Candidate, error)
}

// Service combines normalized candidates from configured providers.
type Service struct {
	providers []Provider
}

// New constructs a resolver from zero or more authorized providers.
func New(providers ...Provider) *Service {
	return &Service{providers: providers}
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
