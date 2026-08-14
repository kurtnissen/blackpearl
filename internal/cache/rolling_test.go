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

func TestNewPersistentRangeRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcd"))
	tests := []struct {
		name    string
		options cache.PersistentRangeOptions
	}{
		{name: "relative root", options: cache.PersistentRangeOptions{Root: "cache", ChunkBytes: 4, FetchTimeout: time.Second}},
		{name: "zero chunk", options: cache.PersistentRangeOptions{Root: t.TempDir(), ChunkBytes: 0, FetchTimeout: time.Second}},
		{name: "zero timeout", options: cache.PersistentRangeOptions{Root: t.TempDir(), ChunkBytes: 4, FetchTimeout: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := cache.NewPersistentRange(context.Background(), test.options, opener)

			require.Error(t, err)
		})
	}
}

func TestPersistentRangeRetainsEveryFetchedChunkWithoutEviction(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))
	source, err := cache.NewPersistentRange(context.Background(), cache.PersistentRangeOptions{
		Root: root, ChunkBytes: 4, FetchTimeout: time.Second,
	}, opener)
	require.NoError(t, err)
	handle := openRollingHandle(t, source, 12)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	require.Equal(t, "ijkl", readRollingExact(t, handle, 8, 4))
	require.Equal(t, "efgh", readRollingExact(t, handle, 4, 4))
	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))

	stats := source.Stats()
	require.Equal(t, int64(12), stats.CurrentBytes)
	require.Equal(t, int64(3), stats.ChunkCount)
	require.Equal(t, int64(12), stats.HighWaterBytes)
	require.Zero(t, stats.Evictions)
	require.Equal(t, 1, opener.readCount(0))
	require.DirExists(t, filepath.Join(root, "persistent"))
	require.NoDirExists(t, filepath.Join(root, "rolling"))
}

func TestPersistentRangeRecoversChunksWithoutRefetchingTheirRanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	firstOpener := newFakeRangeOpener([]byte("abcdefghijkl"))
	first, err := cache.NewPersistentRange(context.Background(), cache.PersistentRangeOptions{
		Root: root, ChunkBytes: 4, FetchTimeout: time.Second,
	}, firstOpener)
	require.NoError(t, err)
	firstHandle := openRollingHandle(t, first, 12)
	require.Equal(t, "abcd", readRollingExact(t, firstHandle, 0, 4))
	require.Equal(t, "ijkl", readRollingExact(t, firstHandle, 8, 4))

	secondOpener := newFakeRangeOpener([]byte("abcdefghijkl"))
	second, err := cache.NewPersistentRange(context.Background(), cache.PersistentRangeOptions{
		Root: root, ChunkBytes: 4, FetchTimeout: time.Second,
	}, secondOpener)
	require.NoError(t, err)
	secondHandle := openRollingHandle(t, second, 12)

	require.Equal(t, "abcd", readRollingExact(t, secondHandle, 0, 4))
	require.Equal(t, "ijkl", readRollingExact(t, secondHandle, 8, 4))
	require.Zero(t, secondOpener.readCount(0))
	require.Zero(t, secondOpener.readCount(8))
	require.Equal(t, int64(8), second.Stats().CurrentBytes)
	require.Zero(t, second.Stats().Evictions)
}

func TestPersistentAndRollingRangeCachesUseSeparateNamespaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opener := newFakeRangeOpener([]byte("abcdefgh"))
	persistent, err := cache.NewPersistentRange(context.Background(), cache.PersistentRangeOptions{
		Root: root, ChunkBytes: 4, FetchTimeout: time.Second,
	}, opener)
	require.NoError(t, err)
	handle := openRollingHandle(t, persistent, 8)
	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))

	rolling, err := cache.NewRolling(context.Background(), cache.RollingOptions{
		Root: root, MaxBytes: 4, ChunkBytes: 4, FetchTimeout: time.Second,
	}, opener)
	require.NoError(t, err)

	require.Equal(t, int64(4), persistent.Stats().CurrentBytes)
	require.Zero(t, rolling.Stats().CurrentBytes)
	require.DirExists(t, filepath.Join(root, "persistent"))
	require.DirExists(t, filepath.Join(root, "rolling"))
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

func TestRollingSourceReadAheadFollowsForegroundReadsAndMovesAfterSeek(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijklmnopqrstuvwxyz012345"))
	source, _ := newRollingSourceWithReadAheadForTest(t, opener, 32, 4, 2)
	handle := openRollingHandle(t, source, 32)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	require.Eventually(t, func() bool {
		return opener.readCount(4) == 1 && opener.readCount(8) == 1
	}, time.Second, 5*time.Millisecond)

	require.Equal(t, "uvwx", readRollingExact(t, handle, 20, 4))
	require.Eventually(t, func() bool {
		stats := source.Stats()
		return opener.readCount(24) == 1 && opener.readCount(28) == 1 && stats.ReservedBytes == 0
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, uint64(4), source.Stats().ReadAheadFetches)
}

func TestRollingSourceForegroundReadJoinsInflightReadAhead(t *testing.T) {
	t.Parallel()
	opener := newOffsetBlockingRangeOpener([]byte("abcdefghijkl"), 4)
	source, _ := newRollingSourceWithReadAheadForTest(t, opener, 12, 4, 1)
	handle := openRollingHandle(t, source, 12)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	select {
	case <-opener.started:
	case <-time.After(time.Second):
		require.FailNow(t, "read-ahead did not start")
	}
	result := make(chan string, 1)
	go func() {
		buffer := make([]byte, 4)
		count, err := handle.ReadAt(context.Background(), buffer, 4)
		result <- fmt.Sprintf("%d:%v:%s", count, err, buffer)
	}()
	close(opener.release)

	require.Equal(t, "4:<nil>:efgh", <-result)
	require.Equal(t, 1, opener.readCount(4))
	require.Eventually(t, func() bool {
		return opener.readCount(8) == 1 && source.Stats().ReservedBytes == 0
	}, time.Second, 5*time.Millisecond)
}

func TestRollingSourceReadAheadPreservesForegroundHeadroom(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))
	source, _ := newRollingSourceWithReadAheadForTest(t, opener, 8, 4, 1)
	handle := openRollingHandle(t, source, 12)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	time.Sleep(20 * time.Millisecond)

	require.Zero(t, opener.readCount(4))
	require.Zero(t, source.Stats().ReadAheadFetches)
	require.LessOrEqual(t, source.Stats().CurrentBytes+source.Stats().ReservedBytes, int64(4))
}

func TestRollingSourceReadAheadContinuesAfterCacheSaturates(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijklmnopqrst"))
	source, _ := newRollingSourceWithReadAheadForTest(t, opener, 12, 4, 1)
	handle := openRollingHandle(t, source, 20)

	require.Equal(t, "abcd", readRollingExact(t, handle, 0, 4))
	require.Eventually(t, func() bool { return opener.readCount(4) == 1 && source.Stats().ReservedBytes == 0 }, time.Second, 5*time.Millisecond)
	require.Equal(t, "ijkl", readRollingExact(t, handle, 8, 4))
	require.Eventually(t, func() bool { return opener.readCount(12) == 1 && source.Stats().ReservedBytes == 0 }, time.Second, 5*time.Millisecond)

	require.Equal(t, "ijkl", readRollingExact(t, handle, 8, 4))
	require.Equal(t, 1, opener.readCount(8))
	require.GreaterOrEqual(t, source.Stats().Evictions, uint64(2))
	require.LessOrEqual(t, source.Stats().CurrentBytes+source.Stats().ReservedBytes, int64(8))
}

func TestRollingSourcePrefetchStagesBoundedMediaPrefix(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijkl"))
	source, _ := newRollingSourceWithPoliciesForTest(t, opener, 12, 4, 0, 2)
	media := rollingMovie(t, 12, "next.mp4")

	source.Prefetch(context.Background(), media)
	require.Eventually(t, func() bool {
		stats := source.Stats()
		return opener.readCount(0) == 1 && opener.readCount(4) == 1 && stats.ReservedBytes == 0
	}, time.Second, 5*time.Millisecond)
	source.Prefetch(context.Background(), media)
	time.Sleep(20 * time.Millisecond)

	require.Equal(t, uint64(2), source.Stats().NextEpisodeFetches)
	require.Zero(t, source.Stats().NextEpisodeErrors)
	require.Equal(t, 1, opener.readCount(0))
	require.Equal(t, 1, opener.readCount(4))
	require.Zero(t, opener.readCount(8))
}

func TestRollingSourcePrefetchRespectsQuotaHeadroomAndCancellation(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijklmnop"))
	source, _ := newRollingSourceWithPoliciesForTest(t, opener, 12, 4, 0, 3)
	media := rollingMovie(t, 16, "next.mp4")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	source.Prefetch(cancelled, media)
	source.Prefetch(context.Background(), media)
	require.Eventually(t, func() bool { return source.Stats().ReservedBytes == 0 && source.Stats().NextEpisodeFetches == 2 }, time.Second, 5*time.Millisecond)

	require.Equal(t, int64(8), source.Stats().CurrentBytes)
	require.LessOrEqual(t, source.Stats().HighWaterBytes, int64(8))
}

func TestRollingSourcePrefetchDoesNotEvictForegroundChunks(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefghijklmnop"))
	source, _ := newRollingSourceWithPoliciesForTest(t, opener, 12, 4, 0, 3)
	current := rollingMovie(t, 16, "current.mp4")
	next := rollingMovie(t, 16, "next.mp4")
	handle, err := source.Open(context.Background(), current)
	require.NoError(t, err)
	buffer := make([]byte, 4)
	_, err = handle.ReadAt(context.Background(), buffer, 0)
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	source.Prefetch(context.Background(), next)
	require.Eventually(t, func() bool {
		stats := source.Stats()
		return stats.ReservedBytes == 0 && stats.NextEpisodeFetches == 1
	}, time.Second, 5*time.Millisecond)

	require.Zero(t, source.Stats().Evictions)
	require.Equal(t, int64(8), source.Stats().CurrentBytes)
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

func TestRollingPoolSharesQuotaAcrossProviderRuntimes(t *testing.T) {
	t.Parallel()
	pool, err := cache.NewRollingPool(context.Background(), cache.RollingOptions{
		Root: t.TempDir(), MaxBytes: 8, ChunkBytes: 4, FetchTimeout: time.Second,
	})
	require.NoError(t, err)
	first, err := pool.Source(newFakeRangeOpener([]byte("abcdefgh")))
	require.NoError(t, err)
	second, err := pool.Source(newFakeRangeOpener([]byte("ijklmnop")))
	require.NoError(t, err)
	firstMedia, err := domain.NewMovie("first", "First", 2026, ".mp4", 8, domain.BackingRef{Provider: "http-range", ObjectID: "first.mp4"})
	require.NoError(t, err)
	secondMedia, err := domain.NewMovie("second", "Second", 2026, ".mp4", 8, domain.BackingRef{Provider: "http-range", ObjectID: "second.mp4"})
	require.NoError(t, err)
	firstHandle, err := first.Open(context.Background(), firstMedia)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, firstHandle.Close()) })
	secondHandle, err := second.Open(context.Background(), secondMedia)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, secondHandle.Close()) })

	require.Equal(t, "abcd", readRollingExact(t, firstHandle, 0, 4))
	require.Equal(t, "ijkl", readRollingExact(t, secondHandle, 0, 4))
	require.Equal(t, "mnop", readRollingExact(t, secondHandle, 4, 4))

	firstStats := first.Stats()
	secondStats := second.Stats()
	require.Equal(t, firstStats, secondStats)
	require.LessOrEqual(t, firstStats.CurrentBytes+firstStats.ReservedBytes, int64(8))
	require.LessOrEqual(t, firstStats.HighWaterBytes, int64(8))
	require.Equal(t, uint64(1), firstStats.Evictions)
}

func TestRollingSourceRejectsChangedObjectValidator(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefgh"))
	source, _ := newRollingSourceForTest(t, opener, 8, 4)
	handle := openRollingHandle(t, source, 8)
	opener.setValidator("fake-v2")

	_, err := handle.ReadAt(context.Background(), make([]byte, 4), 0)

	require.ErrorContains(t, err, "validator changed")
	require.Zero(t, source.Stats().CurrentBytes)
}

func TestRollingSourceAccountsPublishedChunkWhenRemoteCloseFails(t *testing.T) {
	t.Parallel()
	opener := newFakeRangeOpener([]byte("abcdefgh"))
	source, _ := newRollingSourceForTest(t, opener, 8, 4)
	handle := openRollingHandle(t, source, 8)
	opener.setCloseError(errors.New("close failed"))

	_, err := handle.ReadAt(context.Background(), make([]byte, 4), 0)

	require.ErrorContains(t, err, "close remote range source")
	stats := source.Stats()
	require.Equal(t, int64(4), stats.CurrentBytes)
	require.Zero(t, stats.ReservedBytes)
	require.Equal(t, int64(1), stats.ChunkCount)
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
	object := rollingObjectKey("http-range", "movie.mp4", "fake-v1")
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
	return newRollingSourceWithReadAheadForTest(t, opener, maxBytes, chunkBytes, 0)
}

func newRollingSourceWithReadAheadForTest(t *testing.T, opener cache.RangeOpener, maxBytes int64, chunkBytes int64, readAheadChunks int) (*cache.RollingSource, string) {
	return newRollingSourceWithPoliciesForTest(t, opener, maxBytes, chunkBytes, readAheadChunks, 0)
}

func newRollingSourceWithPoliciesForTest(t *testing.T, opener cache.RangeOpener, maxBytes int64, chunkBytes int64, readAheadChunks int, nextEpisodeChunks int) (*cache.RollingSource, string) {
	t.Helper()
	root := t.TempDir()
	source, err := cache.NewRolling(context.Background(), cache.RollingOptions{
		Root:                      root,
		MaxBytes:                  maxBytes,
		ChunkBytes:                chunkBytes,
		ReadAheadChunks:           readAheadChunks,
		NextEpisodePrefetchChunks: nextEpisodeChunks,
		FetchTimeout:              time.Second,
	}, opener)
	require.NoError(t, err)
	return source, root
}

func rollingMovie(t *testing.T, size int64, objectID string) domain.Media {
	t.Helper()
	media, err := domain.NewMovie("rolling", "Rolling", 2026, ".mp4", size, domain.BackingRef{
		Provider: "http-range",
		ObjectID: objectID,
	})
	require.NoError(t, err)
	return media
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
	content   []byte
	mu        sync.Mutex
	reads     map[int64]int
	validator string
	closeErr  error
}

type blockingRangeOpener struct {
	*fakeRangeOpener
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type offsetBlockingRangeOpener struct {
	*fakeRangeOpener
	offset  int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newOffsetBlockingRangeOpener(content []byte, offset int64) *offsetBlockingRangeOpener {
	return &offsetBlockingRangeOpener{
		fakeRangeOpener: newFakeRangeOpener(content),
		offset:          offset,
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
}

func (o *offsetBlockingRangeOpener) Open(ctx context.Context, _ domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	validator := o.validator
	o.mu.Unlock()
	return &offsetBlockingRangeSource{opener: o, reader: bytes.NewReader(o.content), validator: validator}, nil
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
	return &fakeRangeOpener{content: append([]byte(nil), content...), reads: make(map[int64]int), validator: "fake-v1"}
}

func (f *fakeRangeOpener) Open(ctx context.Context, _ domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	validator := f.validator
	closeErr := f.closeErr
	f.mu.Unlock()
	return &fakeRangeSource{opener: f, reader: bytes.NewReader(f.content), validator: validator, closeErr: closeErr}, nil
}

func (f *fakeRangeOpener) Ready(ctx context.Context) error {
	return ctx.Err()
}

func (f *fakeRangeOpener) readCount(offset int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[offset]
}

func (f *fakeRangeOpener) setValidator(validator string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validator = validator
}

func (f *fakeRangeOpener) setCloseError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeErr = err
}

type fakeRangeSource struct {
	opener    *fakeRangeOpener
	reader    *bytes.Reader
	validator string
	closeErr  error
	closed    bool
	mu        sync.Mutex
}

type blockingRangeSource struct {
	*blockingRangeOpener
	reader *bytes.Reader
	closed atomic.Bool
}

type offsetBlockingRangeSource struct {
	opener    *offsetBlockingRangeOpener
	reader    *bytes.Reader
	validator string
}

func (s *offsetBlockingRangeSource) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if offset == s.opener.offset {
		s.opener.once.Do(func() { close(s.opener.started) })
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-s.opener.release:
		}
	}
	s.opener.mu.Lock()
	s.opener.reads[offset]++
	s.opener.mu.Unlock()
	return s.reader.ReadAt(destination, offset)
}

func (s *offsetBlockingRangeSource) Size() int64 { return s.reader.Size() }

func (s *offsetBlockingRangeSource) Validator() string { return s.validator }

func (s *offsetBlockingRangeSource) Close() error { return nil }

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

func (b *blockingRangeSource) Validator() string {
	return "fake-v1"
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

func rollingObjectKey(provider string, objectID string, validator string) string {
	hash := sha256.Sum256([]byte(provider + "\x00" + objectID + "\x00" + validator))
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

func (f *fakeRangeSource) Validator() string {
	return f.validator
}

func (f *fakeRangeSource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}
