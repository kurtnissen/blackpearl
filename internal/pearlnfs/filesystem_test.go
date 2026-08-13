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

func TestFilesystemImplementsReadOnlyBillyContract(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())

	require.Equal(t, billy.ReadCapability|billy.SeekCapability, billy.Capabilities(filesystem))
	require.Equal(t, "/", filesystem.Root())
	require.Equal(t, "Movies/title", filesystem.Join("Movies", "title"))
	_, err := filesystem.Create("new.mp4")
	require.ErrorIs(t, err, billy.ErrReadOnly)
	require.ErrorIs(t, filesystem.Rename(testVirtualPath, "renamed.mp4"), billy.ErrReadOnly)
	require.ErrorIs(t, filesystem.Remove(testVirtualPath), billy.ErrReadOnly)
	_, err = filesystem.TempFile("/", "temporary")
	require.ErrorIs(t, err, billy.ErrReadOnly)
	require.ErrorIs(t, filesystem.Symlink(testVirtualPath, "link.mp4"), billy.ErrReadOnly)
	_, err = filesystem.Readlink(testVirtualPath)
	require.ErrorIs(t, err, billy.ErrNotSupported)
	root, err := filesystem.Chroot("/")
	require.NoError(t, err)
	require.Equal(t, filesystem, root)
	_, err = filesystem.Chroot("Movies")
	require.ErrorIs(t, err, os.ErrNotExist)
	info, err := filesystem.Lstat(testVirtualPath)
	require.NoError(t, err)
	require.Equal(t, "BlackPearl POC (2026).mp4", info.Name())
	require.Equal(t, int64(0), info.ModTime().Unix())
	require.Nil(t, info.Sys())
	_, err = filesystem.ReadDir(testVirtualPath)
	require.Error(t, err)
}

func TestFilesystemFileSupportsSequentialReadsAndAllSeekOrigins(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())
	file, err := filesystem.Open(testVirtualPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	require.Equal(t, testVirtualPath, file.Name())

	buffer := make([]byte, 4)
	count, err := file.Read(buffer)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{0, 1, 2, 3}, buffer)

	position, err := file.Seek(12, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(12), position)
	position, err = file.Seek(4, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(16), position)
	position, err = file.Seek(-4, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, testLogicalSize-4, position)
	count, err = file.Read(buffer)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{252, 253, 254, 255}, buffer)

	_, err = file.Seek(-1, io.SeekStart)
	require.ErrorContains(t, err, "negative")
	_, err = file.Seek(0, 99)
	require.ErrorContains(t, err, "invalid whence")
	_, err = file.Write([]byte("blocked"))
	require.ErrorIs(t, err, billy.ErrReadOnly)
	require.NoError(t, file.Lock())
	require.NoError(t, file.Unlock())
	require.ErrorIs(t, file.Truncate(1), billy.ErrReadOnly)
}

func TestFilesystemRejectsInvalidCatalogSnapshots(t *testing.T) {
	t.Parallel()
	validBacking, err := domain.NewBackingRef("generated", "object")
	require.NoError(t, err)
	tests := []struct {
		name    string
		catalog pearlnfs.Catalog
		message string
	}{
		{
			name:    "list error",
			catalog: catalogStub{listErr: errors.New("database unavailable")},
			message: "list catalog",
		},
		{
			name: "unsafe path",
			catalog: catalogStub{items: []domain.Media{{
				VirtualPath: "Shows/not-a-movie/file.mp4",
				Backing:     validBacking,
			}}},
			message: "invalid virtual path",
		},
		{
			name: "duplicate path",
			catalog: catalogStub{items: []domain.Media{
				{VirtualPath: testVirtualPath, Backing: validBacking},
				{VirtualPath: testVirtualPath, Backing: validBacking},
			}},
			message: "duplicate virtual path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := pearlnfs.New(context.Background(), test.catalog)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestFilesystemRejectsCanceledContextAndNilCatalog(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pearlnfs.New(ctx, catalogStub{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = pearlnfs.New(context.Background(), nil)
	require.ErrorContains(t, err, "catalog is required")
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

type catalogStub struct {
	items   []domain.Media
	listErr error
}

func (c catalogStub) List(context.Context) ([]domain.Media, error) {
	return c.items, c.listErr
}

func (c catalogStub) Open(context.Context, string) (domain.ReadHandle, error) {
	return nil, domain.ErrNotFound
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
