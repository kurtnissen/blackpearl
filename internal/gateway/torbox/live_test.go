package torbox

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLiveAuthorizedTorrentRanges(t *testing.T) {
	if os.Getenv("BLACKPEARL_TORBOX_LIVE") != "1" {
		t.Skip("set BLACKPEARL_TORBOX_LIVE=1 for the opt-in TorBox check")
	}
	token := os.Getenv("BLACKPEARL_TORBOX_API_TOKEN")
	object := os.Getenv("BLACKPEARL_RANGE_OBJECT_ID")
	require.NotEmpty(t, token)
	require.NotEmpty(t, object)
	gateway, err := New(Options{
		APIBaseURL:  "https://api.torbox.app/v1/api/",
		APIToken:    token,
		MetadataTTL: time.Minute,
		LinkTTL:     2 * time.Hour,
	}, &http.Client{Timeout: 30 * time.Second})
	require.NoError(t, err)
	backing, err := domain.NewBackingRef(providerName, object)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	opened, err := gateway.Open(ctx, backing)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	require.Greater(t, opened.Size(), int64(131072))

	for _, offset := range []int64{0, opened.Size() / 10, opened.Size() / 2, opened.Size() - 65536} {
		first := make([]byte, 65536)
		count, readErr := opened.ReadAt(ctx, first, offset)
		require.NoError(t, readErr)
		require.Equal(t, len(first), count)
		second := make([]byte, len(first))
		count, readErr = opened.ReadAt(ctx, second, offset)
		require.NoError(t, readErr)
		require.Equal(t, len(second), count)
		require.True(t, bytes.Equal(first, second), "repeated range at offset %d changed", offset)
	}
}
