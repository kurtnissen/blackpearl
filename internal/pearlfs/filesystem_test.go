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

func TestNewAcceptsCanonicalTVEpisodeLayout(t *testing.T) {
	t.Parallel()
	backing, err := domain.NewBackingRef("generated", "episode")
	require.NoError(t, err)
	episode, err := domain.NewEpisode("episode", "Example Show", 2024, 1, 2, "The Second", ".mkv", 84, backing)
	require.NoError(t, err)

	root, err := New(context.Background(), &fakeCatalog{media: []domain.Media{episode}})

	require.NoError(t, err)
	require.Equal(t, []string{episode.VirtualPath}, root.virtualPaths())
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

func TestNewReportsCatalogAndDuplicatePathFailures(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, "id", "Movie", 10, "key")
	tests := []struct {
		name    string
		catalog *fakeCatalog
		message string
	}{
		{name: "catalog", catalog: &fakeCatalog{listErr: errors.New("database unavailable")}, message: "list catalog for PearlFS"},
		{name: "duplicate", catalog: &fakeCatalog{media: []domain.Media{media, media}}, message: "duplicate virtual path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(context.Background(), test.catalog)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestDirectoryNodesReportReadOnlyPermissions(t *testing.T) {
	t.Parallel()
	root := &Root{}
	var rootAttributes fuse.AttrOut
	var directoryAttributes fuse.AttrOut

	rootErrno := root.Getattr(context.Background(), nil, &rootAttributes)
	directoryErrno := (&directoryNode{}).Getattr(context.Background(), nil, &directoryAttributes)

	require.Zero(t, rootErrno)
	require.Zero(t, directoryErrno)
	require.Equal(t, uint32(syscall.S_IFDIR|0o555), rootAttributes.Mode)
	require.Equal(t, uint32(syscall.S_IFDIR|0o555), directoryAttributes.Mode)
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

func TestFileNodeMapsProviderFailureToEIOAndOpensReadableMedia(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, "id", "Movie", 10, "key")
	failing := &fileNode{media: media, catalog: &fakeCatalog{openErr: errors.New("provider unavailable")}}

	_, _, errno := failing.Open(context.Background(), syscall.O_RDONLY)

	require.Equal(t, syscall.EIO, errno)
	readerCatalog := &fakeCatalog{content: map[string][]byte{media.VirtualPath: []byte("0123456789")}}
	readable := &fileNode{media: media, catalog: readerCatalog}
	handle, flags, errno := readable.Open(context.Background(), syscall.O_RDONLY)
	require.Zero(t, errno)
	require.Equal(t, uint32(fuse.FOPEN_KEEP_CACHE), flags)
	require.NotNil(t, handle)
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

func TestFileHandleRejectsInvalidOffsetAndMapsReadAndCloseFailures(t *testing.T) {
	t.Parallel()
	readFailure := errors.New("backing read failed")
	handle := &fileHandle{reader: &errorReader{readErr: readFailure, closeErr: syscall.EBUSY}}

	result, errno := handle.Read(context.Background(), make([]byte, 1), -1)
	require.Nil(t, result)
	require.Equal(t, syscall.EINVAL, errno)
	result, errno = handle.Read(context.Background(), make([]byte, 1), 0)
	require.Nil(t, result)
	require.Equal(t, syscall.EIO, errno)
	require.Equal(t, syscall.EBUSY, handle.Release(context.Background()))
}

func TestMountValidatesInputsBeforeCallingKernel(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Mount(cancelled, "/tmp/blackpearl", &Root{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = Mount(context.Background(), "relative", &Root{})
	require.ErrorContains(t, err, "must be absolute")
	_, err = Mount(context.Background(), "/tmp/blackpearl", nil)
	require.ErrorContains(t, err, "root is required")
}

func TestInodeForIsDeterministicAndReservedValuesAreAvoided(t *testing.T) {
	t.Parallel()
	first := inodeFor("Movies/Movie/Movie.mp4")
	second := inodeFor("Movies/Movie/Movie.mp4")

	require.Equal(t, first, second)
	require.GreaterOrEqual(t, first, uint64(2))
}

type fakeCatalog struct {
	media   []domain.Media
	content map[string][]byte
	openErr error
	listErr error
}

func (f *fakeCatalog) List(context.Context) ([]domain.Media, error) {
	return f.media, f.listErr
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

type errorReader struct {
	readErr  error
	closeErr error
}

func (r *errorReader) ReadAt(context.Context, []byte, int64) (int, error) {
	return 0, r.readErr
}

func (r *errorReader) Size() int64 {
	return 1
}

func (r *errorReader) Close() error {
	return r.closeErr
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
