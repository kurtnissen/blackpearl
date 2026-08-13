package cache

import (
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
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const rollingDirectory = "rolling"

// RangeOpener opens provider-neutral remote objects for exact reads.
type RangeOpener interface {
	Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error)
	Ready(ctx context.Context) error
}

// RollingOptions configures fixed-size local chunk retention.
type RollingOptions struct {
	Root         string
	MaxBytes     int64
	ChunkBytes   int64
	FetchTimeout time.Duration
}

// Stats is a concurrency-safe snapshot of rolling cache behavior.
type Stats struct {
	CurrentBytes   int64
	ReservedBytes  int64
	HighWaterBytes int64
	ChunkCount     int64
	Hits           uint64
	Misses         uint64
	Fetches        uint64
	Evictions      uint64
}

// RollingSource stores independently addressable remote object chunks under a
// hard byte quota.
type RollingSource struct {
	lifecycle context.Context
	options   RollingOptions
	opener    RangeOpener
	root      string

	mu        sync.Mutex
	chunks    map[chunkKey]*chunkEntry
	current   int64
	reserved  int64
	highWater int64
	tick      uint64
	hits      uint64
	misses    uint64
	fetches   uint64
	evictions uint64
}

type chunkKey struct {
	object string
	index  int64
}

type chunkEntry struct {
	path       string
	size       int64
	pins       int64
	lastAccess uint64
}

// NewRolling creates a rolling cache rooted at an explicit absolute path.
func NewRolling(ctx context.Context, options RollingOptions, opener RangeOpener) (*RollingSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create rolling cache: %w", err)
	}
	if !filepath.IsAbs(options.Root) {
		return nil, fmt.Errorf("rolling cache root must be absolute: %q", options.Root)
	}
	if options.MaxBytes <= 0 {
		return nil, errors.New("rolling cache maximum bytes must be positive")
	}
	if options.ChunkBytes <= 0 || options.ChunkBytes > options.MaxBytes {
		return nil, errors.New("rolling cache chunk bytes must be positive and no larger than maximum bytes")
	}
	if options.FetchTimeout <= 0 {
		return nil, errors.New("rolling cache fetch timeout must be positive")
	}
	if opener == nil {
		return nil, errors.New("rolling cache range opener is required")
	}
	root := filepath.Join(options.Root, rollingDirectory)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create rolling cache root: %w", err)
	}
	return &RollingSource{
		lifecycle: ctx,
		options:   options,
		opener:    opener,
		root:      root,
		chunks:    make(map[chunkKey]*chunkEntry),
	}, nil
}

// Open creates a logical read handle without downloading the complete object.
func (s *RollingSource) Open(ctx context.Context, media domain.Media) (_ domain.ReadHandle, openErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open rolling media: %w", err)
	}
	remote, err := s.opener.Open(ctx, media.Backing)
	if err != nil {
		return nil, fmt.Errorf("open rolling backing source: %w", err)
	}
	if remote.Size() != media.Size {
		closeErr := remote.Close()
		return nil, errors.Join(
			fmt.Errorf("rolling source size mismatch: catalog=%d provider=%d", media.Size, remote.Size()),
			closeErr,
		)
	}
	return &rollingHandle{owner: s, media: media, remote: remote}, nil
}

// Ready verifies the rolling directory and configured range opener.
func (s *RollingSource) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check rolling cache readiness: %w", err)
	}
	info, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("stat rolling cache root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rolling cache root is not a directory: %s", s.root)
	}
	if err := s.opener.Ready(ctx); err != nil {
		return fmt.Errorf("range opener is not ready: %w", err)
	}
	return nil
}

// Stats returns current quota accounting and cache counters.
func (s *RollingSource) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		CurrentBytes:   s.current,
		ReservedBytes:  s.reserved,
		HighWaterBytes: s.highWater,
		ChunkCount:     int64(len(s.chunks)),
		Hits:           s.hits,
		Misses:         s.misses,
		Fetches:        s.fetches,
		Evictions:      s.evictions,
	}
}

type rollingHandle struct {
	owner     *RollingSource
	media     domain.Media
	remote    acquisition.RangeSource
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (h *rollingHandle) Size() int64 {
	return h.media.Size
}

func (h *rollingHandle) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if h.closed.Load() {
		return 0, errors.New("rolling read handle is closed")
	}
	if offset < 0 {
		return 0, errors.New("rolling read offset must not be negative")
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if offset >= h.media.Size {
		return 0, io.EOF
	}
	wanted := int64(len(destination))
	partial := false
	if remaining := h.media.Size - offset; wanted > remaining {
		wanted = remaining
		partial = true
	}
	written := 0
	for int64(written) < wanted {
		logicalOffset := offset + int64(written)
		chunkIndex := logicalOffset / h.owner.options.ChunkBytes
		withinChunk := logicalOffset % h.owner.options.ChunkBytes
		chunkLength := h.owner.chunkLength(h.media.Size, chunkIndex)
		copyLength := chunkLength - withinChunk
		if remaining := wanted - int64(written); copyLength > remaining {
			copyLength = remaining
		}
		entry, err := h.owner.acquireChunk(ctx, h.remote, h.media.Backing, chunkIndex, chunkLength)
		if err != nil {
			return written, err
		}
		count, readErr := readChunk(entry.path, destination[written:written+int(copyLength)], withinChunk)
		h.owner.releaseChunk(entry)
		written += count
		if readErr != nil {
			return written, fmt.Errorf("read rolling chunk %d: %w", chunkIndex, readErr)
		}
	}
	if partial {
		return written, io.EOF
	}
	return written, nil
}

func (h *rollingHandle) Close() error {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		h.closeErr = h.remote.Close()
	})
	return h.closeErr
}

func (s *RollingSource) acquireChunk(
	ctx context.Context,
	remote acquisition.RangeSource,
	backing domain.BackingRef,
	index int64,
	expected int64,
) (*chunkEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := chunkKey{object: objectCacheKey(backing), index: index}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.chunks[key]; ok {
		s.tick++
		entry.lastAccess = s.tick
		entry.pins++
		s.hits++
		return entry, nil
	}
	s.misses++
	if err := s.reserveLocked(expected); err != nil {
		return nil, err
	}
	entry, err := s.fetchChunkLocked(ctx, remote, key, index*s.options.ChunkBytes, expected)
	if err != nil {
		s.reserved -= expected
		return nil, err
	}
	s.reserved -= expected
	s.current += expected
	s.fetches++
	s.tick++
	entry.lastAccess = s.tick
	entry.pins = 1
	s.chunks[key] = entry
	return entry, nil
}

func (s *RollingSource) reserveLocked(expected int64) error {
	for s.current+s.reserved+expected > s.options.MaxBytes {
		key, entry, found := s.oldestUnpinnedLocked()
		if !found {
			return errors.New("rolling cache capacity is pinned")
		}
		if err := os.Remove(entry.path); err != nil {
			return fmt.Errorf("evict rolling chunk: %w", err)
		}
		delete(s.chunks, key)
		s.current -= entry.size
		s.evictions++
	}
	s.reserved += expected
	if usage := s.current + s.reserved; usage > s.highWater {
		s.highWater = usage
	}
	return nil
}

func (s *RollingSource) oldestUnpinnedLocked() (chunkKey, *chunkEntry, bool) {
	var selectedKey chunkKey
	var selected *chunkEntry
	for key, entry := range s.chunks {
		if entry.pins != 0 {
			continue
		}
		if selected == nil || entry.lastAccess < selected.lastAccess {
			selectedKey = key
			selected = entry
		}
	}
	return selectedKey, selected, selected != nil
}

func (s *RollingSource) fetchChunkLocked(
	ctx context.Context,
	remote acquisition.RangeSource,
	key chunkKey,
	offset int64,
	expected int64,
) (_ *chunkEntry, fetchErr error) {
	objectDirectory := filepath.Join(s.root, key.object)
	if err := os.MkdirAll(objectDirectory, 0o750); err != nil {
		return nil, fmt.Errorf("create rolling object directory: %w", err)
	}
	temporary, err := os.CreateTemp(objectDirectory, ".fetch-*")
	if err != nil {
		return nil, fmt.Errorf("create rolling temporary chunk: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			fetchErr = errors.Join(fetchErr, os.Remove(temporaryPath))
		}
	}()
	buffer := make([]byte, expected)
	fetchContext, cancel := context.WithTimeout(ctx, s.options.FetchTimeout)
	count, readErr := remote.ReadAt(fetchContext, buffer, offset)
	cancel()
	if readErr != nil && !(errors.Is(readErr, io.EOF) && int64(count) == expected) {
		closeErr := temporary.Close()
		return nil, errors.Join(fmt.Errorf("fetch rolling range at %d: %w", offset, readErr), closeErr)
	}
	if int64(count) != expected {
		closeErr := temporary.Close()
		return nil, errors.Join(fmt.Errorf("rolling range length mismatch: got %d want %d", count, expected), closeErr)
	}
	if _, err := temporary.Write(buffer); err != nil {
		closeErr := temporary.Close()
		return nil, errors.Join(fmt.Errorf("write rolling temporary chunk: %w", err), closeErr)
	}
	if err := temporary.Sync(); err != nil {
		closeErr := temporary.Close()
		return nil, errors.Join(fmt.Errorf("sync rolling temporary chunk: %w", err), closeErr)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("close rolling temporary chunk: %w", err)
	}
	destination := filepath.Join(objectDirectory, fmt.Sprintf("%016d.chunk", key.index))
	if err := os.Rename(temporaryPath, destination); err != nil {
		return nil, fmt.Errorf("publish rolling chunk: %w", err)
	}
	keepTemporary = false
	if err := os.Chmod(destination, 0o640); err != nil {
		return nil, fmt.Errorf("set rolling chunk permissions: %w", err)
	}
	if err := syncDirectory(objectDirectory); err != nil {
		return nil, err
	}
	return &chunkEntry{path: destination, size: expected}, nil
}

func (s *RollingSource) releaseChunk(entry *chunkEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.pins--
}

func (s *RollingSource) chunkLength(logicalSize int64, index int64) int64 {
	start := index * s.options.ChunkBytes
	remaining := logicalSize - start
	if remaining < s.options.ChunkBytes {
		return remaining
	}
	return s.options.ChunkBytes
}

func readChunk(path string, destination []byte, offset int64) (count int, readErr error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open rolling chunk: %w", err)
	}
	defer func() {
		readErr = errors.Join(readErr, file.Close())
	}()
	count, err = file.ReadAt(destination, offset)
	if errors.Is(err, io.EOF) && count == len(destination) {
		err = nil
	}
	return count, err
}

func objectCacheKey(backing domain.BackingRef) string {
	hash := sha256.Sum256([]byte(backing.Provider + "\x00" + backing.ObjectID))
	return hex.EncodeToString(hash[:])
}

var _ Reader = (*rollingHandle)(nil)
