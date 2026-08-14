package state_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/state"
	"github.com/stretchr/testify/require"
)

func TestRepositoryPersistsUpsertedMediaAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "blackpearl.db")
	repository, err := state.Open(ctx, dbPath)
	require.NoError(t, err)
	media := mustMovie(t, "second", "Zulu", "key-2")
	require.NoError(t, repository.Upsert(ctx, media))
	updated := media
	updated.Backing = domain.BackingRef{Provider: "pearlcache", ObjectID: "updated-key"}
	require.NoError(t, repository.Upsert(ctx, updated))
	require.NoError(t, repository.Close())

	reopened, err := state.Open(ctx, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	actual, err := reopened.GetByVirtualPath(ctx, media.VirtualPath)

	require.NoError(t, err)
	require.Equal(t, updated, actual)
}

func TestRepositoryListsMediaByVirtualPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := state.Open(ctx, filepath.Join(t.TempDir(), "blackpearl.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	zulu := mustMovie(t, "zulu", "Zulu", "key-z")
	alpha := mustMovie(t, "alpha", "Alpha", "key-a")
	require.NoError(t, repository.Upsert(ctx, zulu))
	require.NoError(t, repository.Upsert(ctx, alpha))

	actual, err := repository.List(ctx)

	require.NoError(t, err)
	require.Equal(t, []domain.Media{alpha, zulu}, actual)
}

func TestRepositoryMapsMissingPathToDomainError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := state.Open(ctx, filepath.Join(t.TempDir(), "blackpearl.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })

	_, err = repository.GetByVirtualPath(ctx, "Movies/Missing/Missing.mp4")

	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestRepositoryPingHonorsClosedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := state.Open(ctx, filepath.Join(t.TempDir(), "blackpearl.db"))
	require.NoError(t, err)
	require.NoError(t, repository.Ping(ctx))
	require.NoError(t, repository.Close())

	err = repository.Ping(ctx)

	require.Error(t, err)
	require.False(t, errors.Is(err, domain.ErrNotFound))
}

func TestOpenRequiresAbsoluteDatabasePath(t *testing.T) {
	t.Parallel()

	_, err := state.Open(context.Background(), "relative/blackpearl.db")

	require.ErrorContains(t, err, "must be absolute")
}

func TestRepositoryOperationsReportClosedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := state.Open(ctx, filepath.Join(t.TempDir(), "blackpearl.db"))
	require.NoError(t, err)
	require.NoError(t, repository.Close())
	media := mustMovie(t, "id", "Movie", "key")

	require.ErrorContains(t, repository.Upsert(ctx, media), "upsert media")
	_, err = repository.GetByVirtualPath(ctx, media.VirtualPath)
	require.ErrorContains(t, err, "get media by virtual path")
	_, err = repository.List(ctx)
	require.ErrorContains(t, err, "list media")
	require.NoError(t, repository.Close())
}

func TestRepositoryHonorsCancelledOpenAndQueries(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := state.Open(cancelled, filepath.Join(t.TempDir(), "blackpearl.db"))

	require.Error(t, err)
}

func mustMovie(t *testing.T, id domain.MediaID, title string, key string) domain.Media {
	t.Helper()
	media, err := domain.NewMovie(
		id,
		title,
		2026,
		".mp4",
		10,
		domain.BackingRef{Provider: "pearlcache", ObjectID: key},
	)
	require.NoError(t, err)
	return media
}
