package state

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kurtnissen/blackpearl/internal/domain"
)

// MemoryRepository stores one runtime catalog snapshot without sharing mutable
// metadata with a catalog being replaced.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[domain.MediaID]domain.Media
}

// NewMemory creates an empty process-local catalog repository.
func NewMemory() *MemoryRepository {
	return &MemoryRepository{items: make(map[domain.MediaID]domain.Media)}
}

// Upsert creates or replaces one media item.
func (r *MemoryRepository) Upsert(ctx context.Context, media domain.Media) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upsert memory media: %w", err)
	}
	r.mu.Lock()
	r.items[media.ID] = media
	r.mu.Unlock()
	return nil
}

// GetByVirtualPath finds one media item by its immutable virtual path.
func (r *MemoryRepository) GetByVirtualPath(ctx context.Context, virtualPath string) (domain.Media, error) {
	if err := ctx.Err(); err != nil {
		return domain.Media{}, fmt.Errorf("get memory media: %w", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, media := range r.items {
		if media.VirtualPath == virtualPath {
			return media, nil
		}
	}
	return domain.Media{}, fmt.Errorf("%w: media path %q", domain.ErrNotFound, virtualPath)
}

// List returns an independent stable virtual-path snapshot.
func (r *MemoryRepository) List(ctx context.Context) ([]domain.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list memory media: %w", err)
	}
	r.mu.RLock()
	items := make([]domain.Media, 0, len(r.items))
	for _, media := range r.items {
		items = append(items, media)
	}
	r.mu.RUnlock()
	sort.Slice(items, func(left int, right int) bool { return items[left].VirtualPath < items[right].VirtualPath })
	return items, nil
}

// Ping verifies caller cancellation for the process-local repository.
func (r *MemoryRepository) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ping memory catalog: %w", err)
	}
	return nil
}
