package cache_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestRollingSourceCancelledWaiterDoesNotAbortSharedFetch(t *testing.T) {
	t.Parallel()
	opener := newBlockingRangeOpener([]byte("abcdefgh"))
	source, _ := newRollingSourceForTest(t, opener, 8, 4)
	firstHandle := openRollingHandle(t, source, 8)
	secondHandle := openRollingHandle(t, source, 8)
	firstResult := make(chan error, 1)
	go func() {
		_, err := firstHandle.ReadAt(context.Background(), make([]byte, 4), 0)
		firstResult <- err
	}()
	<-opener.started

	base, cancel := context.WithCancel(context.Background())
	observed := newObservedContext(base, 2)
	secondResult := make(chan error, 1)
	go func() {
		_, err := secondHandle.ReadAt(observed, make([]byte, 4), 0)
		secondResult <- err
	}()
	<-observed.reached
	cancel()

	select {
	case err := <-secondResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(250 * time.Millisecond):
		close(opener.release)
		<-firstResult
		<-secondResult
		require.FailNow(t, "cancelled waiter remained blocked behind shared fetch")
	}
	close(opener.release)
	require.NoError(t, <-firstResult)
	require.Equal(t, 1, opener.readCount(0))
}

func TestNewRollingRecoversChunksRemovesTemporaryFilesAndTrimsQuota(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	object := rollingObjectKey("http-range", "movie.mp4")
	objectDirectory := filepath.Join(root, "rolling", object)
	require.NoError(t, os.MkdirAll(objectDirectory, 0o750))
	baseTime := time.Now().Add(-time.Hour)
	for index, content := range []string{"abcd", "efgh", "ijkl"} {
		path := filepath.Join(objectDirectory, formatChunkIndex(int64(index)))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o640))
		stamp := baseTime.Add(time.Duration(index) * time.Minute)
		require.NoError(t, os.Chtimes(path, stamp, stamp))
	}
	temporary := filepath.Join(objectDirectory, ".fetch-abandoned")
	require.NoError(t, os.WriteFile(temporary, []byte("xx"), 0o600))
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))

	source, err := cache.NewRolling(context.Background(), cache.RollingOptions{
		Root:         root,
		MaxBytes:     8,
		ChunkBytes:   4,
		FetchTimeout: time.Second,
	}, opener)

	require.NoError(t, err)
	require.NoFileExists(t, temporary)
	stats := source.Stats()
	require.Equal(t, int64(8), stats.CurrentBytes)
	require.Equal(t, int64(2), stats.ChunkCount)
	require.Equal(t, uint64(1), stats.Evictions)
	require.LessOrEqual(t, stats.HighWaterBytes, int64(8))
	require.NoFileExists(t, filepath.Join(objectDirectory, formatChunkIndex(0)))
	handle := openRollingHandle(t, source, 12)
	require.Equal(t, "efgh", readRollingExact(t, handle, 4, 4))
	require.Zero(t, opener.readCount(4))
}

func newRollingSourceForTest(t *testing.T, opener cache.RangeOpener, maxBytes int64, chunkBytes int64) (*cache.RollingSource, string) {
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

type blockingRangeOpener struct {
	*fakeRangeOpener
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRangeOpener(content []byte) *blockingRangeOpener {
	return &blockingRangeOpener{
		fakeRangeOpener: newFakeRangeOpener(content),
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
}

func (b *blockingRangeOpener) Open(ctx context.Context, _ domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &blockingRangeSource{blockingRangeOpener: b, reader: bytes.NewReader(b.content)}, nil
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

type blockingRangeSource struct {
	*blockingRangeOpener
	reader *bytes.Reader
	closed atomic.Bool
}

func (b *blockingRangeSource) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.release:
	}
	b.mu.Lock()
	b.reads[offset]++
	b.mu.Unlock()
	return b.reader.ReadAt(destination, offset)
}

func (b *blockingRangeSource) Size() int64 {
	return b.reader.Size()
}

func (b *blockingRangeSource) Close() error {
	b.closed.Store(true)
	return nil
}

type observedContext struct {
	context.Context
	target  int64
	checks  atomic.Int64
	reached chan struct{}
	once    sync.Once
}

func newObservedContext(ctx context.Context, target int64) *observedContext {
	return &observedContext{Context: ctx, target: target, reached: make(chan struct{})}
}

func (c *observedContext) Err() error {
	if c.checks.Add(1) >= c.target {
		c.once.Do(func() { close(c.reached) })
	}
	return c.Context.Err()
}

func rollingObjectKey(provider string, objectID string) string {
	hash := sha256.Sum256([]byte(provider + "\x00" + objectID))
	return hex.EncodeToString(hash[:])
}

func formatChunkIndex(index int64) string {
	return fmt.Sprintf("%016d.chunk", index)
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
