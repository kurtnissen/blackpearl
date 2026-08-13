package cache_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewRollingRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcd"))
	tests := []struct {
		name    string
		options cache.RollingOptions
	}{
		{name: "relative root", options: cache.RollingOptions{Root: "cache", MaxBytes: 8, ChunkBytes: 4, FetchTimeout: time.Second}},
		{name: "zero quota", options: cache.RollingOptions{Root: t.TempDir(), MaxBytes: 0, ChunkBytes: 4, FetchTimeout: time.Second}},
		{name: "zero chunk", options: cache.RollingOptions{Root: t.TempDir(), MaxBytes: 8, ChunkBytes: 0, FetchTimeout: time.Second}},
		{name: "chunk exceeds quota", options: cache.RollingOptions{Root: t.TempDir(), MaxBytes: 4, ChunkBytes: 8, FetchTimeout: time.Second}},
		{name: "zero timeout", options: cache.RollingOptions{Root: t.TempDir(), MaxBytes: 8, ChunkBytes: 4, FetchTimeout: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := cache.NewRolling(context.Background(), test.options, opener)

			require.Error(t, err)
		})
	}
}

func TestRollingSourceReadAtReturnsExactNonsequentialRanges(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))
	source, _ := newRollingSourceForTest(t, opener, 8, 4)
	handle := openRollingHandle(t, source, 12)

	buffer := make([]byte, 5)
	count, err := handle.ReadAt(context.Background(), buffer, 2)
	require.NoError(t, err)
	require.Equal(t, 5, count)
	require.Equal(t, "cdefg", string(buffer))

	final := make([]byte, 4)
	count, err = handle.ReadAt(context.Background(), final, 10)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 2, count)
	require.Equal(t, "kl", string(final[:count]))

	count, err = handle.ReadAt(context.Background(), make([]byte, 1), 12)
	require.ErrorIs(t, err, io.EOF)
	require.Zero(t, count)
	require.Equal(t, int64(12), handle.Size())
}

func TestRollingSourceReadAtEvictsWithinHardQuotaAndRefetches(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))
	source, root := newRollingSourceForTest(t, opener, 8, 4)
	handle := openRollingHandle(t, source, 12)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	require.Equal(t, "efgh", readRollingExact(t, handle, 4, 4))
	require.Equal(t, "ijkl", readRollingExact(t, handle, 8, 4))
	stats := source.Stats()
	require.LessOrEqual(t, stats.CurrentBytes+stats.ReservedBytes, int64(8))
	require.LessOrEqual(t, stats.HighWaterBytes, int64(8))
	require.Equal(t, uint64(1), stats.Evictions)
	require.Equal(t, 1, opener.readCount(0))

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	require.Equal(t, 2, opener.readCount(0))
	require.GreaterOrEqual(t, source.Stats().Evictions, uint64(2))

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		require.LessOrEqual(t, info.Size(), int64(4))
		require.NotEqual(t, int64(12), info.Size())
		return nil
	})
	require.NoError(t, err)
}

func TestRollingHandleRejectsInvalidReadsAndClose(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefgh"))
	source, _ := newRollingSourceForTest(t, opener, 8, 4)
	handle := openRollingHandle(t, source, 8)

	count, err := handle.ReadAt(context.Background(), nil, 0)
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = handle.ReadAt(context.Background(), make([]byte, 1), -1)
	require.ErrorContains(t, err, "negative")
	require.NoError(t, handle.Close())
	_, err = handle.ReadAt(context.Background(), make([]byte, 1), 0)
	require.ErrorContains(t, err, "closed")
}

func newRollingSourceForTest(t *testing.T, opener *fakeRangeOpener, maxBytes int64, chunkBytes int64) (*cache.RollingSource, string) {
	t.Helper()
	root := t.TempDir()
	source, err := cache.NewRolling(context.Background(), cache.RollingOptions{
		Root:         root,
		MaxBytes:     maxBytes,
		ChunkBytes:   chunkBytes,
		FetchTimeout: time.Second,
	}, opener)
	require.NoError(t, err)
	return source, root
}

func openRollingHandle(t *testing.T, source *cache.RollingSource, size int64) domain.ReadHandle {
	t.Helper()
	media, err := domain.NewMovie("rolling", "Rolling", 2026, ".mp4", size, domain.BackingRef{
		Provider: "http-range",
		ObjectID: "movie.mp4",
	})
	require.NoError(t, err)
	handle, err := source.Open(context.Background(), media)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, handle.Close())
	})
	return handle
}

func readRollingExact(t *testing.T, handle domain.ReadHandle, offset int64, length int) string {
	t.Helper()
	buffer := make([]byte, length)
	count, err := handle.ReadAt(context.Background(), buffer, offset)
	require.NoError(t, err)
	require.Equal(t, length, count)
	return string(buffer)
}

type fakeRangeOpener struct {
	content []byte
	mu      sync.Mutex
	reads   map[int64]int
}

func newFakeRangeOpener(content []byte) *fakeRangeOpener {
	return &fakeRangeOpener{content: append([]byte(nil), content...), reads: make(map[int64]int)}
}

func (f *fakeRangeOpener) Open(ctx context.Context, _ domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &fakeRangeSource{opener: f, reader: bytes.NewReader(f.content)}, nil
}

func (f *fakeRangeOpener) Ready(ctx context.Context) error {
	return ctx.Err()
}

func (f *fakeRangeOpener) readCount(offset int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[offset]
}

type fakeRangeSource struct {
	opener *fakeRangeOpener
	reader *bytes.Reader
	closed bool
	mu     sync.Mutex
}

func (f *fakeRangeSource) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errors.New("fake source closed")
	}
	f.opener.mu.Lock()
	f.opener.reads[offset]++
	f.opener.mu.Unlock()
	return f.reader.ReadAt(destination, offset)
}

func (f *fakeRangeSource) Size() int64 {
	return f.reader.Size()
}

func (f *fakeRangeSource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
