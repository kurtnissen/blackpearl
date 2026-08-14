package acquisitionjob_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	acquisitionjobrepo "github.com/blackpearl-media/blackpearl/internal/repository/acquisitionjob"
	acquisitionjobservice "github.com/blackpearl-media/blackpearl/internal/service/acquisitionjob"
	"github.com/stretchr/testify/require"
)

func TestManagerSubmitsDeduplicatesGetsAndListsJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := openJobRepository(t, ctx)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	ids := []string{"0123456789abcdef0123456789abcdef", "11111111111111111111111111111111"}
	manager, err := acquisitionjobservice.NewManager(repository, acquisitionjobservice.ManagerOptions{
		Now: func() time.Time { return now },
		NewID: func() (string, error) {
			id := ids[0]
			ids = ids[1:]
			return id, nil
		},
	})
	require.NoError(t, err)
	request := mustJobMovieRequest(t)

	first, created, err := manager.Submit(ctx, request)
	require.NoError(t, err)
	require.True(t, created)
	duplicate, created, err := manager.Submit(ctx, request)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID(), duplicate.ID())

	loaded, err := manager.Get(ctx, first.ID())
	require.NoError(t, err)
	require.Equal(t, first.ID(), loaded.ID())
	jobs, err := manager.List(ctx, 20)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
}

func openJobRepository(t *testing.T, ctx context.Context) *acquisitionjobrepo.Repository {
	t.Helper()
	repository, err := acquisitionjobrepo.Open(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repository.Close()) })
	return repository
}

func mustJobMovieRequest(t *testing.T) acquisition.SearchRequest {
	t.Helper()
	request, err := acquisition.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	return request
}
