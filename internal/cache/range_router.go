package cache

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

// RangeRouter dispatches provider-neutral backing references to their range opener.
type RangeRouter struct {
	providers []string
	openers   map[string]RangeOpener
}

// NewRangeRouter builds an immutable provider routing table.
func NewRangeRouter(openers map[string]RangeOpener) (*RangeRouter, error) {
	if len(openers) == 0 {
		return nil, errors.New("at least one range provider is required")
	}

	copied := make(map[string]RangeOpener, len(openers))
	providers := make([]string, 0, len(openers))
	for provider, opener := range openers {
		if _, err := domain.NewBackingRef(provider, "router-validation"); err != nil {
			return nil, fmt.Errorf("validate range provider: %w", err)
		}
		if opener == nil {
			return nil, fmt.Errorf("range provider %q has no opener", provider)
		}
		copied[provider] = opener
		providers = append(providers, provider)
	}
	sort.Strings(providers)

	return &RangeRouter{providers: providers, openers: copied}, nil
}

// Open routes a backing reference without logging its provider-specific object ID.
func (r *RangeRouter) Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open range provider: %w", err)
	}
	validated, err := domain.NewBackingRef(backing.Provider, backing.ObjectID)
	if err != nil {
		return nil, fmt.Errorf("validate range backing: %w", err)
	}
	opener, ok := r.openers[validated.Provider]
	if !ok {
		return nil, fmt.Errorf("unsupported range provider %q", validated.Provider)
	}
	source, err := opener.Open(ctx, validated)
	if err != nil {
		return nil, fmt.Errorf("open range provider %q: %w", validated.Provider, err)
	}
	return source, nil
}

// Ready verifies every configured provider in stable order.
func (r *RangeRouter) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check range providers: %w", err)
	}
	for _, provider := range r.providers {
		if err := r.openers[provider].Ready(ctx); err != nil {
			return fmt.Errorf("range provider %q is not ready: %w", provider, err)
		}
	}
	return nil
}
