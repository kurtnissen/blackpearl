package state_test

import (
	"context"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/state"
	"github.com/stretchr/testify/require"
)

func TestMemoryRepositoryKeepsIndependentCatalogSnapshots(t *testing.T) {
	t.Parallel()
	first := state.NewMemory()
	second := state.NewMemory()
	backing, err := domain.NewBackingRef("torbox-torrent", "17:3")
	require.NoError(t, err)
	oldMedia, err := domain.NewMovie("selected", "Old", 2025, ".mp4", 10, backing)
	require.NoError(t, err)
	newMedia, err := domain.NewMovie("selected", "New", 2026, ".mkv", 20, backing)
	require.NoError(t, err)

	require.NoError(t, first.Upsert(context.Background(), oldMedia))
	require.NoError(t, second.Upsert(context.Background(), newMedia))
	firstItems, err := first.List(context.Background())
	require.NoError(t, err)
	secondItems, err := second.List(context.Background())
	require.NoError(t, err)

	require.Equal(t, []domain.Media{oldMedia}, firstItems)
	require.Equal(t, []domain.Media{newMedia}, secondItems)
	loaded, err := first.GetByVirtualPath(context.Background(), oldMedia.VirtualPath)
	require.NoError(t, err)
	require.Equal(t, oldMedia, loaded)
	_, err = first.GetByVirtualPath(context.Background(), newMedia.VirtualPath)
	require.ErrorIs(t, err, domain.ErrNotFound)
}
