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

const rollingDirectory = "rolling"

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
	inflight  map[chunkKey]*fetchCall
	notify    chan struct{}
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

type fetchCall struct {
	done chan struct{}
	err  error
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
	result := &RollingSource{
		lifecycle: ctx,
		options:   options,
		opener:    opener,
		root:      root,
		chunks:    make(map[chunkKey]*chunkEntry),
		inflight:  make(map[chunkKey]*fetchCall),
		notify:    make(chan struct{}),
	}
	if err := result.recoverChunks(); err != nil {
		return nil, err
	}
	return result, nil
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
	validator string
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
		s.mu.Lock()
		if entry, ok := s.chunks[key]; ok {
			s.tick++
			entry.lastAccess = s.tick
			entry.pins++
			s.hits++
			s.mu.Unlock()
			return entry, nil
		}
		if call, ok := s.inflight[key]; ok {
			done := call.done
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				if call.err != nil {
					return nil, call.err
				}
				continue
			}
		}
		reserved, err := s.tryReserveLocked(expected)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if !reserved {
			notify := s.notify
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-notify:
				continue
			}
		}
		call := &fetchCall{done: make(chan struct{})}
		s.inflight[key] = call
		s.misses++
		s.mu.Unlock()
		go s.runFetch(call, key, backing, validator, logicalSize, index*s.options.ChunkBytes, expected)
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
	for s.current+s.reserved+expected > s.options.MaxBytes {
		key, entry, found := s.oldestUnpinnedLocked()
		if !found {
			return false, nil
		}
		if err := os.Remove(entry.path); err != nil {
			return false, fmt.Errorf("evict rolling chunk: %w", err)
		}
		delete(s.chunks, key)
		s.current -= entry.size
		s.evictions++
	}
	s.reserved += expected
	if usage := s.current + s.reserved; usage > s.highWater {
		s.highWater = usage
	}
	return true, nil
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

func (s *RollingSource) runFetch(
	call *fetchCall,
	key chunkKey,
	backing domain.BackingRef,
	validator string,
	logicalSize int64,
	offset int64,
	expected int64,
) {
	fetchContext, cancel := context.WithTimeout(s.lifecycle, s.options.FetchTimeout)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved -= expected
	if entry != nil {
		s.current += expected
		s.fetches++
		s.tick++
		entry.lastAccess = s.tick
		s.chunks[key] = entry
	}
	call.err = err
	delete(s.inflight, key)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.pins--
	s.signalLocked()
}

func (s *RollingSource) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

type recoveredChunk struct {
	key      chunkKey
	entry    *chunkEntry
	modified time.Time
}

func (s *RollingSource) recoverChunks() error {
	objectDirectories, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("read rolling cache root: %w", err)
	}
	var recovered []recoveredChunk
	for _, objectDirectory := range objectDirectories {
		if !objectDirectory.IsDir() || !rollingObjectPattern.MatchString(objectDirectory.Name()) {
			return fmt.Errorf("unexpected entry in rolling cache root: %q", objectDirectory.Name())
		}
		objectPath := filepath.Join(s.root, objectDirectory.Name())
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
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > s.options.ChunkBytes {
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
		s.tick++
		item.entry.lastAccess = s.tick
		s.chunks[item.key] = item.entry
		s.current += item.entry.size
	}
	for s.current > s.options.MaxBytes {
		key, entry, found := s.oldestUnpinnedLocked()
		if !found {
			return errors.New("rolling cache recovery could not trim quota")
		}
		if err := os.Remove(entry.path); err != nil {
			return fmt.Errorf("trim recovered rolling chunk: %w", err)
		}
		delete(s.chunks, key)
		s.current -= entry.size
		s.evictions++
	}
	s.highWater = s.current
	return nil
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

func objectCacheKey(backing domain.BackingRef, validator string) string {
	hash := sha256.Sum256([]byte(backing.Provider + "\x00" + backing.ObjectID + "\x00" + validator))
	return hex.EncodeToString(hash[:])
}

var _ Reader = (*rollingHandle)(nil)
