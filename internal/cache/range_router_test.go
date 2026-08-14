package cache_test

import (
	"context"
	"io"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRangeRouterDispatchesByBackingProviderAndCopiesMap(t *testing.T) {
	t.Parallel()
	torbox := &routingOpener{source: &routingSource{size: 10}}
	archive := &routingOpener{source: &routingSource{size: 20}}
	openers := map[string]cache.RangeOpener{
		"torbox-torrent":        torbox,
		"internet-archive-file": archive,
	}
	router, err := cache.NewRangeRouter(openers)
	require.NoError(t, err)
	delete(openers, "internet-archive-file")

	opened, err := router.Open(context.Background(), domain.BackingRef{
		Provider: "internet-archive-file", ObjectID: "opaque",
	})

	require.NoError(t, err)
	require.Equal(t, int64(20), opened.Size())
	require.Equal(t, 1, archive.opens)
	require.Zero(t, torbox.opens)
	require.Equal(t, "opaque", archive.backing.ObjectID)
}

func TestRangeRouterRejectsUnknownProviderBeforeOpening(t *testing.T) {
	t.Parallel()
	opener := &routingOpener{source: &routingSource{size: 10}}
	router, err := cache.NewRangeRouter(map[string]cache.RangeOpener{"torbox-torrent": opener})
	require.NoError(t, err)

	_, err = router.Open(context.Background(), domain.BackingRef{Provider: "unknown", ObjectID: "private-object"})

	require.ErrorContains(t, err, "unsupported range provider")
	require.NotContains(t, err.Error(), "private-object")
	require.Zero(t, opener.opens)
}

func TestRangeRouterReadyUsesStableProviderOrder(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	router, err := cache.NewRangeRouter(map[string]cache.RangeOpener{
		"z-provider": &routingOpener{readyName: "z-provider", readyOrder: &order},
		"a-provider": &routingOpener{readyName: "a-provider", readyOrder: &order},
	})
	require.NoError(t, err)

	err = router.Ready(context.Background())

	require.NoError(t, err)
	require.Equal(t, []string{"a-provider", "z-provider"}, order)
}

func TestRangeRouterRejectsInvalidConstructionAndCancellation(t *testing.T) {
	t.Parallel()
	_, err := cache.NewRangeRouter(nil)
	require.Error(t, err)
	_, err = cache.NewRangeRouter(map[string]cache.RangeOpener{"bad provider": &routingOpener{}})
	require.Error(t, err)
	_, err = cache.NewRangeRouter(map[string]cache.RangeOpener{"valid-provider": nil})
	require.Error(t, err)

	opener := &routingOpener{source: &routingSource{size: 1}}
	router, err := cache.NewRangeRouter(map[string]cache.RangeOpener{"valid-provider": opener})
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = router.Open(cancelled, domain.BackingRef{Provider: "valid-provider", ObjectID: "object"})
	require.ErrorIs(t, err, context.Canceled)
	err = router.Ready(cancelled)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, opener.opens)
}

type routingOpener struct {
	source     acquisition.RangeSource
	backing    domain.BackingRef
	opens      int
	readyName  string
	readyOrder *[]string
}

func (o *routingOpener) Open(_ context.Context, backing domain.BackingRef) (acquisition.RangeSource, error) {
	o.opens++
	o.backing = backing
	return o.source, nil
}

func (o *routingOpener) Ready(context.Context) error {
	if o.readyOrder != nil {
		*o.readyOrder = append(*o.readyOrder, o.readyName)
	}
	return nil
}

type routingSource struct {
	size int64
}

func (s *routingSource) ReadAt(context.Context, []byte, int64) (int, error) { return 0, io.EOF }
func (s *routingSource) Size() int64                                        { return s.size }
func (s *routingSource) Validator() string                                  { return "validator" }
func (s *routingSource) Close() error                                       { return nil }
