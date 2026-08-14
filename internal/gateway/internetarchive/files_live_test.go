package internetarchive_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/gateway/internetarchive"
	"github.com/stretchr/testify/require"
)

func TestLiveLicensedArchiveFileSupportsExactRangeRead(t *testing.T) {
	if os.Getenv("BLACKPEARL_LIVE_ARCHIVE") != "1" {
		t.Skip("set BLACKPEARL_LIVE_ARCHIVE=1 for the legal Archive range contract")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	gateway, err := internetarchive.New("https://archive.org/", client)
	require.NoError(t, err)
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "mariposahd_s01e01", Title: "MariposaHD S01E01",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 175_099_607, Indexer: "Internet Archive",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	candidates, err := gateway.ListRangeCandidates(ctx, release)
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	var selected acquisition.RangeCandidate
	for _, candidate := range candidates {
		if candidate.Media().Name == "mariposaHD.S01E01.1080i.en_512kb.mp4" {
			selected = candidate
			break
		}
	}
	require.NotEmpty(t, selected.Media().ObjectID)
	source, err := gateway.Open(ctx, selected.Media().Backing())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })

	buffer := make([]byte, 16)
	n, err := source.ReadAt(ctx, buffer, 0)

	require.NoError(t, err)
	require.Equal(t, len(buffer), n)
}
