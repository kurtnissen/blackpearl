package pearlfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"syscall"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/stretchr/testify/require"
)

func TestNewBuildsDeterministicMovieLayout(t *testing.T) {
	t.Parallel()
	zulu := mustMovie(t, "z", "Zulu", 4, "key-z")
	alpha := mustMovie(t, "a", "Alpha", 5, "key-a")
	catalog := &fakeCatalog{media: []domain.Media{zulu, alpha}}

	root, err := New(context.Background(), catalog)

	require.NoError(t, err)
	require.Equal(t, []string{
		"Movies/Alpha (2026)/Alpha (2026).mp4",
		"Movies/Zulu (2026)/Zulu (2026).mp4",
	}, root.virtualPaths())
}

func TestNewRejectsCatalogPathOutsideMovieHierarchy(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{media: []domain.Media{{
		ID:          "unsafe",
		Type:        domain.MediaTypeMovie,
		VirtualPath: "../outside.mp4",
		Size:        1,
		Backing:     domain.BackingRef{Provider: "pearlcache", ObjectID: "key"},
	}}}

	_, err := New(context.Background(), catalog)

	require.ErrorContains(t, err, "invalid virtual path")
}

func TestFileNodeReportsReadOnlySizeAndRejectsWriteOpen(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, "id", "Movie", 10, "key")
	node := &fileNode{media: media, catalog: &fakeCatalog{}}
	var attributes fuse.AttrOut

	errno := node.Getattr(context.Background(), nil, &attributes)

	require.Zero(t, errno)
	require.Equal(t, uint64(10), attributes.Size)
	require.Equal(t, uint32(syscall.S_IFREG|0o444), attributes.Mode)
	_, _, errno = node.Open(context.Background(), syscall.O_WRONLY)
	require.Equal(t, syscall.EROFS, errno)
	_, supportsWrite := any(node).(fs.NodeWriter)
	require.False(t, supportsWrite)
}

func TestFileHandleSupportsNonsequentialAndEOFReads(t *testing.T) {
	t.Parallel()
	reader := newMemoryReader([]byte("0123456789"))
	handle := &fileHandle{reader: reader}

	result, errno := handle.Read(context.Background(), make([]byte, 3), 6)
	require.Zero(t, errno)
	actual, status := result.Bytes(nil)
	result.Done()
	require.Equal(t, fuse.OK, status)
	require.Equal(t, []byte("678"), actual)

	result, errno = handle.Read(context.Background(), make([]byte, 5), 8)
	require.Zero(t, errno)
	actual, status = result.Bytes(nil)
	result.Done()
	require.Equal(t, fuse.OK, status)
	require.Equal(t, []byte("89"), actual)
	require.Zero(t, handle.Release(context.Background()))
	require.True(t, reader.closed)
}

func TestFileNodeMapsMissingCatalogObjectToENOENT(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, "id", "Movie", 10, "key")
	node := &fileNode{
		media:   media,
		catalog: &fakeCatalog{openErr: fmtError(domain.ErrNotFound)},
	}

	_, _, errno := node.Open(context.Background(), syscall.O_RDONLY)

	require.Equal(t, syscall.ENOENT, errno)
}

func TestFileHandleReadsLargeLogicalObjectWithoutCompleteLocalBytes(t *testing.T) {
	t.Parallel()
	const logicalSize = int64(1 << 40)
	handle := &fileHandle{reader: &generatedReader{size: logicalSize}}

	result, errno := handle.Read(context.Background(), make([]byte, 4), logicalSize-4)

	require.Zero(t, errno)
	actual, status := result.Bytes(nil)
	result.Done()
	require.Equal(t, fuse.OK, status)
	require.Equal(t, []byte{252, 253, 254, 255}, actual)
	require.Equal(t, logicalSize, handle.reader.Size())
}

type fakeCatalog struct {
	media   []domain.Media
	content map[string][]byte
	openErr error
}

func (f *fakeCatalog) List(context.Context) ([]domain.Media, error) {
	return f.media, nil
}

func (f *fakeCatalog) Open(_ context.Context, virtualPath string) (domain.ReadHandle, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	content, ok := f.content[virtualPath]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return newMemoryReader(content), nil
}

type memoryReader struct {
	*bytes.Reader
	closed bool
}

func newMemoryReader(content []byte) *memoryReader {
	return &memoryReader{Reader: bytes.NewReader(content)}
}

func (r *memoryReader) Close() error {
	r.closed = true
	return nil
}

func (r *memoryReader) Size() int64 {
	return r.Reader.Size()
}

func (r *memoryReader) ReadAt(_ context.Context, destination []byte, offset int64) (int, error) {
	return r.Reader.ReadAt(destination, offset)
}

type generatedReader struct {
	size int64
}

func (r *generatedReader) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	count := min(int64(len(destination)), r.size-offset)
	for index := int64(0); index < count; index++ {
		destination[index] = byte(offset + index)
	}
	if count < int64(len(destination)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func (r *generatedReader) Size() int64 {
	return r.size
}

func (r *generatedReader) Close() error {
	return nil
}

func mustMovie(t *testing.T, id domain.MediaID, title string, size int64, key string) domain.Media {
	t.Helper()
	media, err := domain.NewMovie(
		id,
		title,
		2026,
		".mp4",
		size,
		domain.BackingRef{Provider: "pearlcache", ObjectID: key},
	)
	require.NoError(t, err)
	return media
}

func fmtError(target error) error {
	return errors.Join(errors.New("wrapped"), target)
}
