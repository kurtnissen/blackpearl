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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	rollingDirectory              = "rolling"
	persistentRangeDirectory      = "persistent"
	maximumReadAheadChunks        = 64
	maximumNextEpisodeChunkPrefix = 256
)

var (
	rollingObjectPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rollingChunkPattern  = regexp.MustCompile(`^([0-9]{16})\.chunk$`)
)

// RangeOpener opens provider-neutral remote objects for exact reads.
type RangeOpener interface {
	Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error)
	Ready(ctx context.Context) error
}

// RollingOptions configures fixed-size local chunk retention.
type RollingOptions struct {
	Root                      string
	MaxBytes                  int64
	ChunkBytes                int64
	ReadAheadChunks           int
	NextEpisodePrefetchChunks int
	FetchTimeout              time.Duration
}

// PersistentRangeOptions configures non-evicting provider range retention.
// Media remains range-readable before every chunk has been fetched.
type PersistentRangeOptions struct {
	Root                      string
	ChunkBytes                int64
	ReadAheadChunks           int
	NextEpisodePrefetchChunks int
	FetchTimeout              time.Duration
}

// Stats is a concurrency-safe snapshot of rolling cache behavior.
type Stats struct {
	CurrentBytes       int64
	ReservedBytes      int64
	HighWaterBytes     int64
	ChunkCount         int64
	Hits               uint64
	Misses             uint64
	Fetches            uint64
	Evictions          uint64
	ReadAheadFetches   uint64
	ReadAheadErrors    uint64
	NextEpisodeFetches uint64
	NextEpisodeErrors  uint64
}

// RollingSource stores independently addressable remote object chunks under a
// hard byte quota.
type RollingSource struct {
	shared *rollingShared
	opener RangeOpener
}

// RollingPool owns one rolling-cache quota and chunk index shared by all
// provider runtimes created during browser reconfiguration.
type RollingPool struct {
	shared *rollingShared
}

type rollingShared struct {
	lifecycle context.Context
	options   RollingOptions
	root      string
	policy    retentionPolicy

	mu                 sync.Mutex
	chunks             map[chunkKey]*chunkEntry
	inflight           map[chunkKey]*fetchCall
	notify             chan struct{}
	current            int64
	reserved           int64
	highWater          int64
	tick               uint64
	hits               uint64
	misses             uint64
	fetches            uint64
	evictions          uint64
	readAheadFetches   uint64
	readAheadErrors    uint64
	nextEpisodeFetches uint64
	nextEpisodeErrors  uint64
}

type retentionPolicy uint8

const (
	retentionRolling retentionPolicy = iota
	retentionPersistent
)

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

type fetchCall struct {
	done   chan struct{}
	err    error
	kind   backgroundFetchKind
	parent context.Context
}

type backgroundFetchKind uint8

const (
	backgroundFetchNone backgroundFetchKind = iota
	backgroundFetchReadAhead
	backgroundFetchNextEpisode
)

// NewRolling creates a rolling cache rooted at an explicit absolute path.
func NewRolling(ctx context.Context, options RollingOptions, opener RangeOpener) (*RollingSource, error) {
	pool, err := NewRollingPool(ctx, options)
	if err != nil {
		return nil, err
	}
	return pool.Source(opener)
}

// NewPersistentRange creates a non-evicting range cache. It retains each
// verified chunk after first use without requiring the complete object before
// reads can begin.
func NewPersistentRange(ctx context.Context, options PersistentRangeOptions, opener RangeOpener) (*RollingSource, error) {
	pool, err := NewPersistentRangePool(ctx, options)
	if err != nil {
		return nil, err
	}
	return pool.Source(opener)
}

// NewPersistentRangePool creates one process-lifetime owner for retained
// provider ranges shared by every immutable catalog generation.
func NewPersistentRangePool(ctx context.Context, options PersistentRangeOptions) (*RollingPool, error) {
	if options.ChunkBytes <= 0 {
		return nil, errors.New("persistent range cache chunk bytes must be positive")
	}
	common := RollingOptions{
		Root:                      options.Root,
		ChunkBytes:                options.ChunkBytes,
		ReadAheadChunks:           options.ReadAheadChunks,
		NextEpisodePrefetchChunks: options.NextEpisodePrefetchChunks,
		FetchTimeout:              options.FetchTimeout,
	}
	if err := validateCommonRangeOptions(common, "persistent range cache"); err != nil {
		return nil, err
	}
	return newRangePool(ctx, common, retentionPersistent, persistentRangeDirectory)
}

// NewRollingPool creates one process-lifetime cache owner that can serve
// multiple immutable provider runtimes without duplicating quota accounting.
func NewRollingPool(ctx context.Context, options RollingOptions) (*RollingPool, error) {
	if options.MaxBytes <= 0 {
		return nil, errors.New("rolling cache maximum bytes must be positive")
	}
	if options.ChunkBytes <= 0 || options.ChunkBytes > options.MaxBytes {
		return nil, errors.New("rolling cache chunk bytes must be positive and no larger than maximum bytes")
	}
	if err := validateCommonRangeOptions(options, "rolling cache"); err != nil {
		return nil, err
	}
	return newRangePool(ctx, options, retentionRolling, rollingDirectory)
}

func validateCommonRangeOptions(options RollingOptions, label string) error {
	if !filepath.IsAbs(options.Root) {
		return fmt.Errorf("%s root must be absolute: %q", label, options.Root)
	}
	if options.ReadAheadChunks < 0 || options.ReadAheadChunks > maximumReadAheadChunks {
		return fmt.Errorf("%s read-ahead chunks must be between 0 and %d", label, maximumReadAheadChunks)
	}
	if options.NextEpisodePrefetchChunks < 0 || options.NextEpisodePrefetchChunks > maximumNextEpisodeChunkPrefix {
		return fmt.Errorf("%s next-episode chunks must be between 0 and %d", label, maximumNextEpisodeChunkPrefix)
	}
	if options.FetchTimeout <= 0 {
		return fmt.Errorf("%s fetch timeout must be positive", label)
	}
	return nil
}

func newRangePool(ctx context.Context, options RollingOptions, policy retentionPolicy, directory string) (*RollingPool, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create range cache pool: %w", err)
	}
	root := filepath.Join(options.Root, directory)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create range cache root: %w", err)
	}
	shared := &rollingShared{
		lifecycle: ctx,
		options:   options,
		root:      root,
		policy:    policy,
		chunks:    make(map[chunkKey]*chunkEntry),
		inflight:  make(map[chunkKey]*fetchCall),
		notify:    make(chan struct{}),
	}
	pool := &RollingPool{shared: shared}
	if err := pool.recoverChunks(); err != nil {
		return nil, err
	}
	return pool, nil
}

// Source binds an immutable provider gateway to the shared rolling cache.
func (p *RollingPool) Source(opener RangeOpener) (*RollingSource, error) {
	if p == nil || p.shared == nil {
		return nil, errors.New("rolling cache pool is required")
	}
	if opener == nil {
		return nil, errors.New("rolling cache range opener is required")
	}
	return &RollingSource{shared: p.shared, opener: opener}, nil
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
	return &rollingHandle{owner: s, media: media, remote: remote, validator: remote.Validator()}, nil
}

// Ready verifies the rolling directory and configured range opener.
func (s *RollingSource) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check rolling cache readiness: %w", err)
	}
	info, err := os.Stat(s.shared.root)
	if err != nil {
		return fmt.Errorf("stat rolling cache root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rolling cache root is not a directory: %s", s.shared.root)
	}
	if err := s.opener.Ready(ctx); err != nil {
		return fmt.Errorf("range opener is not ready: %w", err)
	}
	return nil
}

// Stats returns current quota accounting and cache counters.
func (s *RollingSource) Stats() Stats {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	return Stats{
		CurrentBytes:       s.shared.current,
		ReservedBytes:      s.shared.reserved,
		HighWaterBytes:     s.shared.highWater,
		ChunkCount:         int64(len(s.shared.chunks)),
		Hits:               s.shared.hits,
		Misses:             s.shared.misses,
		Fetches:            s.shared.fetches,
		Evictions:          s.shared.evictions,
		ReadAheadFetches:   s.shared.readAheadFetches,
		ReadAheadErrors:    s.shared.readAheadErrors,
		NextEpisodeFetches: s.shared.nextEpisodeFetches,
		NextEpisodeErrors:  s.shared.nextEpisodeErrors,
	}
}

type rollingHandle struct {
	owner     *RollingSource
	media     domain.Media
	remote    acquisition.RangeSource
	validator string
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	readAheadMu         sync.Mutex
	readAheadContext    context.Context
	readAheadCancel     context.CancelFunc
	readAheadGeneration uint64
	lastReadEnd         int64
	hasLastRead         bool
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
	readAheadContext, readAheadGeneration := h.beginRead(offset)
	wanted := int64(len(destination))
	partial := false
	if remaining := h.media.Size - offset; wanted > remaining {
		wanted = remaining
		partial = true
	}
	written := 0
	for int64(written) < wanted {
		logicalOffset := offset + int64(written)
		chunkIndex := logicalOffset / h.owner.shared.options.ChunkBytes
		withinChunk := logicalOffset % h.owner.shared.options.ChunkBytes
		chunkLength := h.owner.chunkLength(h.media.Size, chunkIndex)
		copyLength := chunkLength - withinChunk
		if remaining := wanted - int64(written); copyLength > remaining {
			copyLength = remaining
		}
		entry, err := h.owner.acquireChunk(ctx, h.media.Backing, h.validator, h.media.Size, chunkIndex, chunkLength)
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
	lastChunk := (offset + wanted - 1) / h.owner.shared.options.ChunkBytes
	h.finishRead(readAheadContext, readAheadGeneration, offset+int64(written), lastChunk+1)
	if partial {
		return written, io.EOF
	}
	return written, nil
}

func (h *rollingHandle) beginRead(offset int64) (context.Context, uint64) {
	h.readAheadMu.Lock()
	defer h.readAheadMu.Unlock()
	if h.readAheadContext == nil || (h.hasLastRead && offset != h.lastReadEnd) {
		if h.readAheadCancel != nil {
			h.readAheadCancel()
		}
		h.readAheadContext, h.readAheadCancel = context.WithCancel(h.owner.shared.lifecycle)
		h.readAheadGeneration++
	}
	return h.readAheadContext, h.readAheadGeneration
}

func (h *rollingHandle) finishRead(ctx context.Context, generation uint64, end int64, nextChunk int64) {
	h.readAheadMu.Lock()
	if generation != h.readAheadGeneration {
		h.readAheadMu.Unlock()
		return
	}
	h.lastReadEnd = end
	h.hasLastRead = true
	h.readAheadMu.Unlock()
	h.owner.scheduleReadAhead(ctx, h.media.Backing, h.validator, h.media.Size, nextChunk)
}

func (h *rollingHandle) cancelReadAhead() {
	h.readAheadMu.Lock()
	defer h.readAheadMu.Unlock()
	if h.readAheadCancel != nil {
		h.readAheadCancel()
		h.readAheadCancel = nil
		h.readAheadContext = nil
	}
}

func (s *RollingSource) scheduleReadAhead(
	ctx context.Context,
	backing domain.BackingRef,
	validator string,
	logicalSize int64,
	startIndex int64,
) {
	protected := chunkKey{object: objectCacheKey(backing, validator), index: startIndex - 1}
	s.scheduleBackgroundChunks(
		ctx, backing, validator, logicalSize, startIndex, s.shared.options.ReadAheadChunks,
		backgroundFetchReadAhead, &protected,
	)
}

func (s *RollingSource) scheduleBackgroundChunks(
	ctx context.Context,
	backing domain.BackingRef,
	validator string,
	logicalSize int64,
	startIndex int64,
	count int,
	kind backgroundFetchKind,
	protected *chunkKey,
) {
	for distance := 0; distance < count; distance++ {
		if ctx.Err() != nil {
			return
		}
		index := startIndex + int64(distance)
		expected := s.chunkLength(logicalSize, index)
		if expected <= 0 {
			return
		}
		key := chunkKey{object: objectCacheKey(backing, validator), index: index}
		s.shared.mu.Lock()
		if _, exists := s.shared.chunks[key]; exists {
			s.shared.mu.Unlock()
			continue
		}
		if _, exists := s.shared.inflight[key]; exists {
			s.shared.mu.Unlock()
			continue
		}
		if !s.tryReserveBackgroundLocked(expected, kind, protected) {
			s.shared.mu.Unlock()
			return
		}
		call := &fetchCall{done: make(chan struct{}), kind: kind, parent: ctx}
		s.shared.inflight[key] = call
		s.recordBackgroundFetchLocked(kind)
		s.shared.mu.Unlock()
		go s.runFetch(call, key, backing, validator, logicalSize, index*s.shared.options.ChunkBytes, expected)
	}
}

func (s *RollingSource) tryReserveBackgroundLocked(expected int64, kind backgroundFetchKind, protected *chunkKey) bool {
	if s.shared.policy == retentionPersistent {
		s.reserveLocked(expected)
		return true
	}
	foregroundHeadroom := s.shared.options.ChunkBytes
	if kind == backgroundFetchNextEpisode && s.shared.current+s.shared.reserved+expected > s.shared.options.MaxBytes-foregroundHeadroom {
		return false
	}
	for s.shared.current+s.shared.reserved+expected > s.shared.options.MaxBytes-foregroundHeadroom {
		key, entry, found := s.oldestUnpinnedExceptLocked(protected)
		if !found {
			return false
		}
		if err := os.Remove(entry.path); err != nil {
			s.recordBackgroundErrorLocked(kind)
			return false
		}
		delete(s.shared.chunks, key)
		s.shared.current -= entry.size
		s.shared.evictions++
	}
	s.reserveLocked(expected)
	return true
}

func (s *RollingSource) oldestUnpinnedExceptLocked(protected *chunkKey) (chunkKey, *chunkEntry, bool) {
	var selectedKey chunkKey
	var selected *chunkEntry
	for key, entry := range s.shared.chunks {
		if (protected != nil && key == *protected) || entry.pins != 0 {
			continue
		}
		if selected == nil || entry.lastAccess < selected.lastAccess {
			selectedKey = key
			selected = entry
		}
	}
	return selectedKey, selected, selected != nil
}

func (s *RollingSource) recordBackgroundFetchLocked(kind backgroundFetchKind) {
	switch kind {
	case backgroundFetchReadAhead:
		s.shared.readAheadFetches++
	case backgroundFetchNextEpisode:
		s.shared.nextEpisodeFetches++
	}
}

func (s *RollingSource) recordBackgroundErrorLocked(kind backgroundFetchKind) {
	switch kind {
	case backgroundFetchReadAhead:
		s.shared.readAheadErrors++
	case backgroundFetchNextEpisode:
		s.shared.nextEpisodeErrors++
	}
}

// Prefetch schedules a bounded prefix of one logical media object. It never
// blocks or changes foreground read results.
func (s *RollingSource) Prefetch(ctx context.Context, media domain.Media) {
	if ctx.Err() != nil || s.shared.options.NextEpisodePrefetchChunks == 0 {
		return
	}
	go s.prefetchMediaPrefix(media)
}

func (s *RollingSource) prefetchMediaPrefix(media domain.Media) {
	fetchContext, cancel := context.WithTimeout(s.shared.lifecycle, s.shared.options.FetchTimeout)
	defer cancel()
	remote, err := s.opener.Open(fetchContext, media.Backing)
	if err == nil && remote.Size() != media.Size {
		err = fmt.Errorf("rolling prefetch size mismatch: catalog=%d provider=%d", media.Size, remote.Size())
	}
	validator := ""
	if err == nil {
		validator = remote.Validator()
	}
	if remote != nil {
		if closeErr := remote.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close rolling prefetch source: %w", closeErr))
		}
	}
	if err != nil {
		s.shared.mu.Lock()
		s.shared.nextEpisodeErrors++
		s.shared.mu.Unlock()
		return
	}
	s.scheduleBackgroundChunks(
		s.shared.lifecycle, media.Backing, validator, media.Size, 0, s.shared.options.NextEpisodePrefetchChunks,
		backgroundFetchNextEpisode, nil,
	)
}

func (h *rollingHandle) Close() error {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		h.cancelReadAhead()
		h.closeErr = h.remote.Close()
	})
	return h.closeErr
}

func (s *RollingSource) acquireChunk(
	ctx context.Context,
	backing domain.BackingRef,
	validator string,
	logicalSize int64,
	index int64,
	expected int64,
) (*chunkEntry, error) {
	key := chunkKey{object: objectCacheKey(backing, validator), index: index}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.shared.mu.Lock()
		if entry, ok := s.shared.chunks[key]; ok {
			s.shared.tick++
			entry.lastAccess = s.shared.tick
			entry.pins++
			s.shared.hits++
			s.shared.mu.Unlock()
			return entry, nil
		}
		if call, ok := s.shared.inflight[key]; ok {
			done := call.done
			kind := call.kind
			s.shared.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				if call.err != nil {
					if kind != backgroundFetchNone && errors.Is(call.err, context.Canceled) {
						continue
					}
					return nil, call.err
				}
				continue
			}
		}
		reserved, err := s.tryReserveLocked(expected)
		if err != nil {
			s.shared.mu.Unlock()
			return nil, err
		}
		if !reserved {
			notify := s.shared.notify
			s.shared.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-notify:
				continue
			}
		}
		call := &fetchCall{done: make(chan struct{}), parent: s.shared.lifecycle}
		s.shared.inflight[key] = call
		s.shared.misses++
		s.shared.mu.Unlock()
		go s.runFetch(call, key, backing, validator, logicalSize, index*s.shared.options.ChunkBytes, expected)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
		}
	}
}

func (s *RollingSource) tryReserveLocked(expected int64) (bool, error) {
	if s.shared.policy == retentionPersistent {
		s.reserveLocked(expected)
		return true, nil
	}
	for s.shared.current+s.shared.reserved+expected > s.shared.options.MaxBytes {
		key, entry, found := s.oldestUnpinnedLocked()
		if !found {
			return false, nil
		}
		if err := os.Remove(entry.path); err != nil {
			return false, fmt.Errorf("evict rolling chunk: %w", err)
		}
		delete(s.shared.chunks, key)
		s.shared.current -= entry.size
		s.shared.evictions++
	}
	s.reserveLocked(expected)
	return true, nil
}

func (s *RollingSource) reserveLocked(expected int64) {
	s.shared.reserved += expected
	if usage := s.shared.current + s.shared.reserved; usage > s.shared.highWater {
		s.shared.highWater = usage
	}
}

func (s *RollingSource) oldestUnpinnedLocked() (chunkKey, *chunkEntry, bool) {
	var selectedKey chunkKey
	var selected *chunkEntry
	for key, entry := range s.shared.chunks {
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

func (s *RollingSource) runFetch(
	call *fetchCall,
	key chunkKey,
	backing domain.BackingRef,
	validator string,
	logicalSize int64,
	offset int64,
	expected int64,
) {
	fetchContext, cancel := context.WithTimeout(call.parent, s.shared.options.FetchTimeout)
	defer cancel()
	remote, err := s.opener.Open(fetchContext, backing)
	if err == nil && remote.Size() != logicalSize {
		err = fmt.Errorf("rolling source size changed: catalog=%d provider=%d", logicalSize, remote.Size())
	}
	if err == nil && remote.Validator() != validator {
		err = fmt.Errorf("rolling source validator changed: opened=%q provider=%q", validator, remote.Validator())
	}
	var entry *chunkEntry
	if err == nil {
		entry, err = s.fetchChunk(fetchContext, remote, key, offset, expected)
	}
	if remote != nil {
		if closeErr := remote.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close remote range source: %w", closeErr))
		}
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	s.shared.reserved -= expected
	if entry != nil {
		s.shared.current += expected
		s.shared.fetches++
		s.shared.tick++
		entry.lastAccess = s.shared.tick
		s.shared.chunks[key] = entry
	}
	call.err = err
	if err != nil && call.kind != backgroundFetchNone {
		s.recordBackgroundErrorLocked(call.kind)
	}
	delete(s.shared.inflight, key)
	close(call.done)
	s.signalLocked()
}

func (s *RollingSource) fetchChunk(
	ctx context.Context,
	remote acquisition.RangeSource,
	key chunkKey,
	offset int64,
	expected int64,
) (_ *chunkEntry, fetchErr error) {
	objectDirectory := filepath.Join(s.shared.root, key.object)
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
	count, readErr := remote.ReadAt(ctx, buffer, offset)
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
	keepDestination := false
	defer func() {
		if !keepDestination {
			fetchErr = errors.Join(fetchErr, os.Remove(destination))
		}
	}()
	if err := os.Chmod(destination, 0o640); err != nil {
		return nil, fmt.Errorf("set rolling chunk permissions: %w", err)
	}
	if err := syncDirectory(objectDirectory); err != nil {
		return nil, err
	}
	keepDestination = true
	return &chunkEntry{path: destination, size: expected}, nil
}

func (s *RollingSource) releaseChunk(entry *chunkEntry) {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	entry.pins--
	s.signalLocked()
}

func (s *RollingSource) signalLocked() {
	close(s.shared.notify)
	s.shared.notify = make(chan struct{})
}

type recoveredChunk struct {
	key      chunkKey
	entry    *chunkEntry
	modified time.Time
}

func (p *RollingPool) recoverChunks() error {
	objectDirectories, err := os.ReadDir(p.shared.root)
	if err != nil {
		return fmt.Errorf("read rolling cache root: %w", err)
	}
	var recovered []recoveredChunk
	for _, objectDirectory := range objectDirectories {
		if !objectDirectory.IsDir() || !rollingObjectPattern.MatchString(objectDirectory.Name()) {
			return fmt.Errorf("unexpected entry in rolling cache root: %q", objectDirectory.Name())
		}
		objectPath := filepath.Join(p.shared.root, objectDirectory.Name())
		entries, readErr := os.ReadDir(objectPath)
		if readErr != nil {
			return fmt.Errorf("read rolling object directory: %w", readErr)
		}
		for _, entry := range entries {
			entryPath := filepath.Join(objectPath, entry.Name())
			if strings.HasPrefix(entry.Name(), ".fetch-") {
				if removeErr := os.Remove(entryPath); removeErr != nil {
					return fmt.Errorf("remove abandoned rolling fetch: %w", removeErr)
				}
				continue
			}
			matches := rollingChunkPattern.FindStringSubmatch(entry.Name())
			if len(matches) != 2 || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return fmt.Errorf("unexpected entry in rolling object directory: %q", entry.Name())
			}
			index, parseErr := strconv.ParseInt(matches[1], 10, 64)
			if parseErr != nil {
				return fmt.Errorf("parse rolling chunk index %q: %w", entry.Name(), parseErr)
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat rolling chunk: %w", infoErr)
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > p.shared.options.ChunkBytes {
				return fmt.Errorf("invalid rolling chunk %q with size %d", entry.Name(), info.Size())
			}
			recovered = append(recovered, recoveredChunk{
				key:      chunkKey{object: objectDirectory.Name(), index: index},
				entry:    &chunkEntry{path: entryPath, size: info.Size()},
				modified: info.ModTime(),
			})
		}
	}
	sort.Slice(recovered, func(left int, right int) bool {
		if recovered[left].modified.Equal(recovered[right].modified) {
			return recovered[left].entry.path < recovered[right].entry.path
		}
		return recovered[left].modified.Before(recovered[right].modified)
	})
	for _, item := range recovered {
		p.shared.tick++
		item.entry.lastAccess = p.shared.tick
		p.shared.chunks[item.key] = item.entry
		p.shared.current += item.entry.size
	}
	for p.shared.policy == retentionRolling && p.shared.current > p.shared.options.MaxBytes {
		key, entry, found := p.oldestUnpinnedLocked()
		if !found {
			return errors.New("rolling cache recovery could not trim quota")
		}
		if err := os.Remove(entry.path); err != nil {
			return fmt.Errorf("trim recovered rolling chunk: %w", err)
		}
		delete(p.shared.chunks, key)
		p.shared.current -= entry.size
		p.shared.evictions++
	}
	p.shared.highWater = p.shared.current
	return nil
}

func (p *RollingPool) oldestUnpinnedLocked() (chunkKey, *chunkEntry, bool) {
	var selectedKey chunkKey
	var selected *chunkEntry
	for key, entry := range p.shared.chunks {
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

func (s *RollingSource) chunkLength(logicalSize int64, index int64) int64 {
	start := index * s.shared.options.ChunkBytes
	remaining := logicalSize - start
	if remaining < s.shared.options.ChunkBytes {
		return remaining
	}
	return s.shared.options.ChunkBytes
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

func objectCacheKey(backing domain.BackingRef, validator string) string {
	hash := sha256.Sum256([]byte(backing.Provider + "\x00" + backing.ObjectID + "\x00" + validator))
	return hex.EncodeToString(hash[:])
}

var _ Reader = (*rollingHandle)(nil)
