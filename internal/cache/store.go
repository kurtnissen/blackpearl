// Package cache provides BlackPearl's content-addressed local object store.
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

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

var cacheKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const backingProvider = "pearlcache"

// Reader supports context-aware random-access reads required by Plex and FUSE.
type Reader = domain.ReadHandle

// Store owns a content-addressed directory.
type Store struct {
	root string
}

// New creates or opens a cache rooted at an explicit path.
func New(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("cache root must be absolute: %q", root)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create cache root: %w", err)
	}
	return &Store{root: root}, nil
}

// Import copies a source into the cache atomically and returns its SHA-256 key.
func (s *Store) Import(ctx context.Context, source string) (backing domain.BackingRef, size int64, err error) {
	input, err := os.Open(source)
	if err != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("open cache import source: %w", err)
	}
	defer func() {
		err = errors.Join(err, input.Close())
	}()

	temporary, err := os.CreateTemp(s.root, ".import-*")
	if err != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("create cache import temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			err = errors.Join(err, os.Remove(temporaryPath))
		}
	}()

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), contextReader{ctx: ctx, reader: input})
	if copyErr != nil {
		closeErr := temporary.Close()
		return domain.BackingRef{}, 0, errors.Join(fmt.Errorf("copy cache import: %w", copyErr), closeErr)
	}
	if syncErr := temporary.Sync(); syncErr != nil {
		closeErr := temporary.Close()
		return domain.BackingRef{}, 0, errors.Join(fmt.Errorf("sync cache import: %w", syncErr), closeErr)
	}
	if closeErr := temporary.Close(); closeErr != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("close cache import: %w", closeErr)
	}

	key := hex.EncodeToString(hasher.Sum(nil))
	destination := s.objectPath(key)
	if renameErr := os.Rename(temporaryPath, destination); renameErr != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("publish cache import: %w", renameErr)
	}
	keepTemporary = false
	if chmodErr := os.Chmod(destination, 0o640); chmodErr != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("set cache object permissions: %w", chmodErr)
	}
	if syncErr := syncDirectory(s.root); syncErr != nil {
		return domain.BackingRef{}, 0, syncErr
	}
	backing, err = domain.NewBackingRef(backingProvider, key)
	if err != nil {
		return domain.BackingRef{}, 0, fmt.Errorf("construct cache backing reference: %w", err)
	}
	return backing, written, nil
}

// Open opens an immutable cache object for random-access reads.
func (s *Store) Open(ctx context.Context, media domain.Media) (Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open cache object: %w", err)
	}
	if media.Backing.Provider != backingProvider {
		return nil, fmt.Errorf("unsupported backing provider: %q", media.Backing.Provider)
	}
	if !cacheKeyPattern.MatchString(media.Backing.ObjectID) {
		return nil, fmt.Errorf("invalid cache object ID: %q", media.Backing.ObjectID)
	}
	file, err := os.Open(s.objectPath(media.Backing.ObjectID))
	if err != nil {
		return nil, fmt.Errorf("open cache object: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("stat cache object: %w", err), closeErr)
	}
	if !info.Mode().IsRegular() {
		closeErr := file.Close()
		return nil, errors.Join(errors.New("cache object is not a regular file"), closeErr)
	}
	return &fileReader{file: file, logicalSize: media.Size}, nil
}

// Ready verifies that the persistent cache root remains accessible.
func (s *Store) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check cache readiness: %w", err)
	}
	info, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("stat cache root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cache root is not a directory: %s", s.root)
	}
	return nil
}

func (s *Store) objectPath(key string) string {
	return filepath.Join(s.root, key+".blob")
}

type fileReader struct {
	file        *os.File
	logicalSize int64
}

func (r *fileReader) Size() int64 {
	return r.logicalSize
}

func (r *fileReader) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.ReadAt(destination, offset)
}

func (r *fileReader) Close() error {
	return r.file.Close()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cache directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		closeErr := directory.Close()
		return errors.Join(fmt.Errorf("sync cache directory: %w", err), closeErr)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close cache directory after sync: %w", err)
	}
	return nil
}
