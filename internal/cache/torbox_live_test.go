package cache_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/torbox"
	"github.com/stretchr/testify/require"
)

func TestLiveRollingCacheReadsAuthorizedTorBoxInteriorRanges(t *testing.T) {
	if os.Getenv("BLACKPEARL_TORBOX_LIVE") != "1" {
		t.Skip("set BLACKPEARL_TORBOX_LIVE=1 for the opt-in TorBox check")
	}
	gateway, err := torbox.New(torbox.Options{
		APIBaseURL:  "https://api.torbox.app/v1/api/",
		APIToken:    os.Getenv("BLACKPEARL_TORBOX_API_TOKEN"),
		MetadataTTL: time.Minute,
		LinkTTL:     2 * time.Hour,
	}, &http.Client{Timeout: 30 * time.Second})
	require.NoError(t, err)
	backing, err := domain.NewBackingRef("torbox-torrent", os.Getenv("BLACKPEARL_RANGE_OBJECT_ID"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	remote, err := gateway.Open(ctx, backing)
	require.NoError(t, err)
	size := remote.Size()
	require.NoError(t, remote.Close())
	rolling, err := cache.NewRolling(ctx, cache.RollingOptions{
		Root:         t.TempDir(),
		MaxBytes:     16 << 20,
		ChunkBytes:   1 << 20,
		FetchTimeout: 30 * time.Second,
	}, gateway)
	require.NoError(t, err)
	media, err := domain.NewMovie("live", "Live", 2026, ".mp4", size, backing)
	require.NoError(t, err)
	handle, err := rolling.Open(ctx, media)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	for _, offset := range []int64{0, size / 10, size / 2, size - 65536} {
		buffer := make([]byte, 65536)
		count, readErr := handle.ReadAt(ctx, buffer, offset)
		require.NoError(t, readErr, "offset %d", offset)
		require.Equal(t, len(buffer), count, "offset %d", offset)
	}
}
