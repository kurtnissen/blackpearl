package core_test

import (
	"context"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCatalogSwitchIsEmptyAndNotReadyBeforeActivation(t *testing.T) {
	t.Parallel()
	switcher := core.NewCatalogSwitch()

	items, err := switcher.List(context.Background())

	require.NoError(t, err)
	require.Empty(t, items)
	_, err = switcher.Open(context.Background(), "Movies/Missing/Missing.mp4")
	require.ErrorIs(t, err, domain.ErrNotConfigured)
	require.ErrorIs(t, switcher.Ready(context.Background()), domain.ErrNotConfigured)
}

func TestCatalogSwitchAtomicallyReplacesDelegate(t *testing.T) {
	t.Parallel()
	first := &fakeCatalogService{items: []domain.Media{{ID: "first"}}}
	second := &fakeCatalogService{items: []domain.Media{{ID: "second"}}}
	switcher := core.NewCatalogSwitch()

	require.Nil(t, switcher.Activate(first))
	items, err := switcher.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, domain.MediaID("first"), items[0].ID)
	require.Same(t, first, switcher.Activate(second))
	items, err = switcher.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, domain.MediaID("second"), items[0].ID)
	require.Same(t, second, switcher.Deactivate())
	items, err = switcher.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, items)
}

type fakeCatalogService struct {
	items []domain.Media
	err   error
}

func (f *fakeCatalogService) List(context.Context) ([]domain.Media, error) {
	return f.items, f.err
}

func (f *fakeCatalogService) Open(context.Context, string) (domain.ReadHandle, error) {
	return nil, f.err
}

func (f *fakeCatalogService) Ready(context.Context) error {
	return f.err
}

var _ core.CatalogService = (*fakeCatalogService)(nil)
