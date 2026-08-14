package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// CatalogService is the complete logical-media boundary switched at runtime.
type CatalogService interface {
	List(ctx context.Context) ([]domain.Media, error)
	Open(ctx context.Context, virtualPath string) (domain.ReadHandle, error)
	Ready(ctx context.Context) error
}

// CatalogSwitch atomically directs new filesystem operations to one catalog.
type CatalogSwitch struct {
	mu       sync.RWMutex
	delegate CatalogService
}

// NewCatalogSwitch creates an inactive catalog that exposes an empty namespace.
func NewCatalogSwitch() *CatalogSwitch {
	return &CatalogSwitch{}
}

// Activate replaces the current delegate and returns the previous one.
func (s *CatalogSwitch) Activate(delegate CatalogService) CatalogService {
	s.mu.Lock()
	previous := s.delegate
	s.delegate = delegate
	s.mu.Unlock()
	return previous
}

// Deactivate removes and returns the current delegate.
func (s *CatalogSwitch) Deactivate() CatalogService {
	return s.Activate(nil)
}

// List returns an empty library while setup is incomplete.
func (s *CatalogSwitch) List(ctx context.Context) ([]domain.Media, error) {
	delegate := s.current()
	if delegate == nil {
		return []domain.Media{}, nil
	}
	return delegate.List(ctx)
}

// Open delegates one logical file open to the active catalog.
func (s *CatalogSwitch) Open(ctx context.Context, virtualPath string) (domain.ReadHandle, error) {
	delegate := s.current()
	if delegate == nil {
		return nil, fmt.Errorf("open media: %w", domain.ErrNotConfigured)
	}
	return delegate.Open(ctx, virtualPath)
}

// Ready reports setup-required until a catalog is active.
func (s *CatalogSwitch) Ready(ctx context.Context) error {
	delegate := s.current()
	if delegate == nil {
		return fmt.Errorf("catalog: %w", domain.ErrNotConfigured)
	}
	return delegate.Ready(ctx)
}

func (s *CatalogSwitch) current() CatalogService {
	s.mu.RLock()
	delegate := s.delegate
	s.mu.RUnlock()
	return delegate
}

var _ CatalogService = (*CatalogSwitch)(nil)
