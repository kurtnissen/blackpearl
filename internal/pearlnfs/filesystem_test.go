package pearlnfs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/pearlnfs"
	"github.com/go-git/go-billy/v5"
	"github.com/stretchr/testify/require"
)

const (
	testVirtualPath = "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4"
	testLogicalSize = int64(1 << 40)
)

func TestFilesystemExposesPlexHierarchyAndLogicalSize(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())

	root, err := filesystem.Stat("/")
	require.NoError(t, err)
	require.True(t, root.IsDir())
	require.Equal(t, os.FileMode(0o555), root.Mode().Perm())

	movies, err := filesystem.ReadDir("Movies")
	require.NoError(t, err)
	require.Len(t, movies, 1)
	require.Equal(t, "BlackPearl POC (2026)", movies[0].Name())
	require.True(t, movies[0].IsDir())

	file, err := filesystem.Stat(testVirtualPath)
	require.NoError(t, err)
	require.Equal(t, testLogicalSize, file.Size())
	require.Equal(t, os.FileMode(0o444), file.Mode().Perm())
}

func TestFilesystemReadsRequestedTailRangeWithoutCompleteFile(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())
	file, err := filesystem.Open(testVirtualPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	destination := make([]byte, 4)

	count, err := file.ReadAt(destination, testLogicalSize-4)

	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{252, 253, 254, 255}, destination)
}

func TestFilesystemReturnsPartialRangeWithEOF(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())
	file, err := filesystem.Open(testVirtualPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	destination := make([]byte, 8)

	count, err := file.ReadAt(destination, testLogicalSize-4)

	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{252, 253, 254, 255}, destination[:count])
}

func TestFilesystemRejectsWritesAndMissingPaths(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())

	_, err := filesystem.OpenFile(testVirtualPath, os.O_WRONLY, 0o600)
	require.ErrorIs(t, err, billy.ErrReadOnly)
	require.ErrorIs(t, filesystem.MkdirAll("Movies/New", 0o755), billy.ErrReadOnly)
	_, err = filesystem.Stat("Movies/Missing/movie.mp4")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestFilesystemPropagatesServiceCancellationToReads(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	filesystem := newTestFilesystem(t, ctx)
	file, err := filesystem.Open(testVirtualPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	cancel()

	_, err = file.ReadAt(make([]byte, 1), 0)

	require.ErrorIs(t, err, context.Canceled)
}

func newTestFilesystem(t *testing.T, ctx context.Context) billy.Filesystem {
	t.Helper()
	backing, err := domain.NewBackingRef("generated", "one-terabyte")
	require.NoError(t, err)
	media, err := domain.NewMovie("poc", "BlackPearl POC", 2026, ".mp4", testLogicalSize, backing)
	require.NoError(t, err)
	filesystem, err := pearlnfs.New(ctx, &generatedCatalog{media: media})
	require.NoError(t, err)
	return filesystem
}

type generatedCatalog struct {
	media domain.Media
}

func (c *generatedCatalog) List(context.Context) ([]domain.Media, error) {
	return []domain.Media{c.media}, nil
}

func (c *generatedCatalog) Open(_ context.Context, virtualPath string) (domain.ReadHandle, error) {
	if virtualPath != c.media.VirtualPath {
		return nil, domain.ErrNotFound
	}
	return &generatedHandle{size: c.media.Size}, nil
}

type generatedHandle struct {
	size   int64
	offset int64
	closed bool
}

func (h *generatedHandle) Size() int64 {
	return h.size
}

func (h *generatedHandle) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if h.closed {
		return 0, errors.New("read closed handle")
	}
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	if offset >= h.size {
		return 0, io.EOF
	}
	count := min(int64(len(destination)), h.size-offset)
	for index := int64(0); index < count; index++ {
		destination[index] = byte(offset + index)
	}
	if count < int64(len(destination)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func (h *generatedHandle) Close() error {
	h.closed = true
	return nil
}
