package cache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/stretchr/testify/require"
)

func TestImportStoresImmutableContentBySHA256(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.mp4")
	original := []byte("blackpearl-test-video-bytes")
	require.NoError(t, os.WriteFile(source, original, 0o600))
	store, err := cache.New(root)
	require.NoError(t, err)

	key, size, err := store.Import(ctx, source)
	require.NoError(t, err)
	expectedHash := sha256.Sum256(original)
	require.Equal(t, hex.EncodeToString(expectedHash[:]), key)
	require.Equal(t, int64(len(original)), size)

	require.NoError(t, os.WriteFile(source, []byte("changed"), 0o600))
	reader, err := store.Open(ctx, key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	actual, err := io.ReadAll(io.NewSectionReader(reader, 0, reader.Size()))
	require.NoError(t, err)
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

	firstKey, _, err := store.Import(ctx, source)
	require.NoError(t, err)
	secondKey, _, err := store.Import(ctx, source)
	require.NoError(t, err)

	require.Equal(t, firstKey, secondKey)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, firstKey+".blob", entries[0].Name())
}

func TestOpenSupportsNonsequentialReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("0123456789"), 0o600))
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)
	key, _, err := store.Import(ctx, source)
	require.NoError(t, err)
	reader, err := store.Open(ctx, key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	buffer := make([]byte, 3)
	count, readErr := reader.ReadAt(buffer, 6)

	require.NoError(t, readErr)
	require.Equal(t, 3, count)
	require.Equal(t, []byte("678"), buffer)
}

func TestOpenRejectsMalformedKeys(t *testing.T) {
	t.Parallel()
	store, err := cache.New(t.TempDir())
	require.NoError(t, err)

	tests := []string{"", "../escape", "ABC", "not-a-sha256"}
	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			_, openErr := store.Open(context.Background(), key)
			require.Error(t, openErr)
		})
	}
}
