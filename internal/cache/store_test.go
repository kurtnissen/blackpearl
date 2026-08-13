package cache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewRequiresAbsoluteRoot(t *testing.T) {
	t.Parallel()

	_, err := cache.New("relative/cache")

	require.ErrorContains(t, err, "must be absolute")
}

func TestImportStoresImmutableContentBySHA256(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.mp4")
	original := []byte("blackpearl-test-video-bytes")
	require.NoError(t, os.WriteFile(source, original, 0o600))
	store, err := cache.New(root)
	require.NoError(t, err)

	backing, size, err := store.Import(ctx, source)
	require.NoError(t, err)
	expectedHash := sha256.Sum256(original)
	require.Equal(t, "pearlcache", backing.Provider)
	require.Equal(t, hex.EncodeToString(expectedHash[:]), backing.ObjectID)
	require.Equal(t, int64(len(original)), size)

	require.NoError(t, os.WriteFile(source, []byte("changed"), 0o600))
	reader, err := store.Open(ctx, mediaFor(t, size, backing))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	actual := make([]byte, reader.Size())
	count, err := reader.ReadAt(ctx, actual, 0)
	require.NoError(t, err)
	require.Equal(t, len(actual), count)
	require.Equal(t, original, actual)
}

func TestImportIsIdempotentAndLeavesNoTemporaryFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("same bytes"), 0o600))
	store, err := cache.New(root)
	require.NoError(t, err)

	firstBacking, _, err := store.Import(ctx, source)
	require.NoError(t, err)
	secondBacking, _, err := store.Import(ctx, source)
	require.NoError(t, err)

	require.Equal(t, firstBacking, secondBacking)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, firstBacking.ObjectID+".blob", entries[0].Name())
}

func TestImportReportsMissingSourceAndCancellation(t *testing.T) {
	t.Parallel()
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)

	_, _, err = store.Import(context.Background(), filepath.Join(t.TempDir(), "missing.mp4"))
	require.ErrorContains(t, err, "open cache import source")
	source := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("content"), 0o600))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = store.Import(cancelled, source)
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenSupportsNonsequentialReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("0123456789"), 0o600))
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)
	backing, size, err := store.Import(ctx, source)
	require.NoError(t, err)
	reader, err := store.Open(ctx, mediaFor(t, size, backing))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	buffer := make([]byte, 3)
	count, readErr := reader.ReadAt(ctx, buffer, 6)

	require.NoError(t, readErr)
	require.Equal(t, 3, count)
	require.Equal(t, []byte("678"), buffer)
}

func TestOpenRejectsMalformedBackingReferences(t *testing.T) {
	t.Parallel()
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)

	tests := []domain.BackingRef{
		{},
		{Provider: "other", ObjectID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Provider: "pearlcache", ObjectID: "../escape"},
		{Provider: "pearlcache", ObjectID: "ABC"},
	}
	for _, backing := range tests {
		t.Run(backing.Provider+"-"+backing.ObjectID, func(t *testing.T) {
			_, openErr := store.Open(context.Background(), domain.Media{Backing: backing, Size: 1})
			require.Error(t, openErr)
		})
	}
}

func TestOpenReportsCancellationAndMissingObjects(t *testing.T) {
	t.Parallel()
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)
	backing := domain.BackingRef{
		Provider: "pearlcache",
		ObjectID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Open(cancelled, domain.Media{Backing: backing, Size: 1})
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.Open(context.Background(), domain.Media{Backing: backing, Size: 1})
	require.ErrorContains(t, err, "open cache object")
}

func TestReadAtHonorsCancelledContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("0123456789"), 0o600))
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)
	backing, size, err := store.Import(ctx, source)
	require.NoError(t, err)
	reader, err := store.Open(ctx, mediaFor(t, size, backing))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	_, err = reader.ReadAt(cancelled, make([]byte, 1), 0)

	require.ErrorIs(t, err, context.Canceled)
}

func TestReadyChecksRootAndContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := cache.New(root)
	require.NoError(t, err)
	require.NoError(t, store.Ready(context.Background()))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, store.Ready(cancelled), context.Canceled)

	require.NoError(t, os.Remove(root))
	require.ErrorContains(t, store.Ready(context.Background()), "stat cache root")
}

func mediaFor(t *testing.T, size int64, backing domain.BackingRef) domain.Media {
	t.Helper()
	media, err := domain.NewMovie("id", "Movie", 2026, ".mp4", size, backing)
	require.NoError(t, err)
	return media
}
