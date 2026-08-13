package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestImportPOCCachesAndPersistsCanonicalMovie(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{}
	cacheStore := &fakeCache{importKey: "cache-key", importSize: 99}
	catalog := core.NewCatalog(repository, cacheStore)

	media, err := catalog.ImportPOC(context.Background(), "/fixture/test.mp4")

	require.NoError(t, err)
	require.Equal(t, domain.MediaID("blackpearl-poc-2026"), media.ID)
	require.Equal(t, "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4", media.VirtualPath)
	require.Equal(t, int64(99), media.Size)
	require.Equal(t, "/fixture/test.mp4", cacheStore.importedSource)
	require.Equal(t, media, repository.upserted)
}

func TestImportPOCWrapsCacheFailureWithoutPersisting(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{}
	cacheStore := &fakeCache{importErr: errors.New("disk full")}
	catalog := core.NewCatalog(repository, cacheStore)

	_, err := catalog.ImportPOC(context.Background(), "/fixture/test.mp4")

	require.ErrorContains(t, err, "import POC fixture")
	require.Empty(t, repository.upserted.ID)
}

func TestOpenResolvesCatalogPathAndCacheObject(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "cache-key")
	repository := &fakeRepository{media: media}
	reader := newMemoryReader([]byte("12345"))
	cacheStore := &fakeCache{reader: reader}
	catalog := core.NewCatalog(repository, cacheStore)

	actual, err := catalog.Open(context.Background(), media.VirtualPath)

	require.NoError(t, err)
	require.Same(t, reader, actual)
	require.Equal(t, media.VirtualPath, repository.lookupPath)
	require.Equal(t, "cache-key", cacheStore.openedKey)
}

func TestOpenRejectsCacheSizeMismatch(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "cache-key")
	reader := newMemoryReader([]byte("1234"))
	catalog := core.NewCatalog(&fakeRepository{media: media}, &fakeCache{reader: reader})

	_, err := catalog.Open(context.Background(), media.VirtualPath)

	require.ErrorContains(t, err, "size mismatch")
	require.True(t, reader.closed)
}

func TestReadyChecksRepositoryAndCachedObjects(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "cache-key")
	reader := newMemoryReader([]byte("12345"))
	repository := &fakeRepository{listed: []domain.Media{media}}
	catalog := core.NewCatalog(repository, &fakeCache{reader: reader})

	err := catalog.Ready(context.Background())

	require.NoError(t, err)
	require.True(t, repository.pinged)
	require.True(t, reader.closed)
}

func TestReadyRequiresAtLeastOneCatalogItem(t *testing.T) {
	t.Parallel()
	catalog := core.NewCatalog(&fakeRepository{}, &fakeCache{})

	err := catalog.Ready(context.Background())

	require.ErrorContains(t, err, "catalog is empty")
}

type fakeRepository struct {
	upserted   domain.Media
	media      domain.Media
	listed     []domain.Media
	lookupPath string
	pingErr    error
	pinged     bool
}

func (f *fakeRepository) Upsert(_ context.Context, media domain.Media) error {
	f.upserted = media
	return nil
}

func (f *fakeRepository) GetByVirtualPath(_ context.Context, path string) (domain.Media, error) {
	f.lookupPath = path
	return f.media, nil
}

func (f *fakeRepository) List(context.Context) ([]domain.Media, error) {
	return f.listed, nil
}

func (f *fakeRepository) Ping(context.Context) error {
	f.pinged = true
	return f.pingErr
}

type fakeCache struct {
	importKey      string
	importSize     int64
	importErr      error
	importedSource string
	openedKey      string
	reader         domain.Reader
}

func (f *fakeCache) Import(_ context.Context, source string) (string, int64, error) {
	f.importedSource = source
	return f.importKey, f.importSize, f.importErr
}

func (f *fakeCache) Open(_ context.Context, key string) (domain.Reader, error) {
	f.openedKey = key
	return f.reader, nil
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

var _ io.ReaderAt = (*memoryReader)(nil)

func mustMovie(t *testing.T, size int64, key string) domain.Media {
	t.Helper()
	media, err := domain.NewMovie("id", "Movie", 2026, ".mp4", size, key)
	require.NoError(t, err)
	return media
}
