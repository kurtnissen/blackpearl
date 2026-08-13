package core_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestImportPOCCachesAndPersistsCanonicalMovie(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{}
	cacheStore := &fakeCache{
		importBacking: domain.BackingRef{Provider: "pearlcache", ObjectID: "object"},
		importSize:    99,
	}
	catalog := core.NewCatalog(repository, cacheStore, cacheStore)

	media, err := catalog.ImportPOC(context.Background(), "/fixture/test.mp4")

	require.NoError(t, err)
	require.Equal(t, domain.MediaID("blackpearl-poc-2026"), media.ID)
	require.Equal(t, "Movies/BlackPearl POC (2026)/BlackPearl POC (2026).mp4", media.VirtualPath)
	require.Equal(t, int64(99), media.Size)
	require.Equal(t, cacheStore.importBacking, media.Backing)
	require.Equal(t, "/fixture/test.mp4", cacheStore.importedSource)
	require.Equal(t, media, repository.upserted)
}

func TestImportPOCWrapsCacheFailureWithoutPersisting(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{}
	cacheStore := &fakeCache{importErr: errors.New("disk full")}
	catalog := core.NewCatalog(repository, cacheStore, cacheStore)

	_, err := catalog.ImportPOC(context.Background(), "/fixture/test.mp4")

	require.ErrorContains(t, err, "import POC fixture")
	require.Empty(t, repository.upserted.ID)
}

func TestOpenResolvesCatalogPathAndCacheObject(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "object")
	repository := &fakeRepository{media: media}
	reader := newMemoryReader([]byte("12345"))
	cacheStore := &fakeCache{reader: reader}
	catalog := core.NewCatalog(repository, cacheStore, cacheStore)

	actual, err := catalog.Open(context.Background(), media.VirtualPath)

	require.NoError(t, err)
	require.Same(t, reader, actual)
	require.Equal(t, media.VirtualPath, repository.lookupPath)
	require.Equal(t, media, cacheStore.openedMedia)
}

func TestOpenRejectsCacheSizeMismatch(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "object")
	reader := newMemoryReader([]byte("1234"))
	cacheStore := &fakeCache{reader: reader}
	catalog := core.NewCatalog(&fakeRepository{media: media}, cacheStore, cacheStore)

	_, err := catalog.Open(context.Background(), media.VirtualPath)

	require.ErrorContains(t, err, "size mismatch")
	require.True(t, reader.closed)
}

func TestReadyChecksRepositoryAndCachedObjects(t *testing.T) {
	t.Parallel()
	media := mustMovie(t, 5, "object")
	repository := &fakeRepository{listed: []domain.Media{media}}
	cacheStore := &fakeCache{}
	catalog := core.NewCatalog(repository, cacheStore, cacheStore)

	err := catalog.Ready(context.Background())

	require.NoError(t, err)
	require.True(t, repository.pinged)
	require.True(t, cacheStore.readyCalled)
}

func TestReadyRequiresAtLeastOneCatalogItem(t *testing.T) {
	t.Parallel()
	cacheStore := &fakeCache{}
	catalog := core.NewCatalog(&fakeRepository{}, cacheStore, cacheStore)

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
	importBacking  domain.BackingRef
	importSize     int64
	importErr      error
	importedSource string
	openedMedia    domain.Media
	reader         domain.ReadHandle
	readyCalled    bool
}

func (f *fakeCache) Import(_ context.Context, source string) (domain.BackingRef, int64, error) {
	f.importedSource = source
	return f.importBacking, f.importSize, f.importErr
}

func (f *fakeCache) Open(_ context.Context, media domain.Media) (domain.ReadHandle, error) {
	f.openedMedia = media
	return f.reader, nil
}

func (f *fakeCache) Ready(context.Context) error {
	f.readyCalled = true
	return nil
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

func mustMovie(t *testing.T, size int64, key string) domain.Media {
	t.Helper()
	media, err := domain.NewMovie(
		"id",
		"Movie",
		2026,
		".mp4",
		size,
		domain.BackingRef{Provider: "pearlcache", ObjectID: key},
	)
	require.NoError(t, err)
	return media
}
