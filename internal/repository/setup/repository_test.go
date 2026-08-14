package setup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	setuprepo "github.com/blackpearl-media/blackpearl/internal/repository/setup"
	"github.com/stretchr/testify/require"
)

func TestRepositorySaveSurvivesReopenWithPrivatePermissions(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "setup")
	repository, err := setuprepo.New(root)
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.mkv", 1234)
	require.NoError(t, err)
	configuration, err := domain.NewSetupConfiguration(candidate, "Example", 2026)
	require.NoError(t, err)

	require.NoError(t, repository.Save(context.Background(), "private-token", configuration))
	reopened, err := setuprepo.New(root)
	require.NoError(t, err)
	token, loaded, err := reopened.Load(context.Background())

	require.NoError(t, err)
	require.Equal(t, "private-token", token)
	require.Equal(t, configuration, loaded)
	directoryInfo, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), directoryInfo.Mode().Perm())
	current, err := os.ReadFile(filepath.Join(root, "current"))
	require.NoError(t, err)
	generationRoot := filepath.Join(root, "generations", string(current[:len(current)-1]))
	for _, name := range []string{"torbox.token", "configuration.json"} {
		info, statErr := os.Stat(filepath.Join(generationRoot, name))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestRepositoryClearAtomicallyReturnsToUnconfiguredState(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "setup")
	repository, err := setuprepo.New(root)
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.mkv", 1234)
	require.NoError(t, err)
	configuration, err := domain.NewSetupConfiguration(candidate, "Example", 2026)
	require.NoError(t, err)
	require.NoError(t, repository.Save(context.Background(), "private-token", configuration))

	require.NoError(t, repository.Clear(context.Background()))
	_, _, err = repository.Load(context.Background())

	require.ErrorIs(t, err, domain.ErrNotFound)
	entries, err := os.ReadDir(filepath.Join(root, "generations"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRepositorySaveRemovesInactiveCredentialGenerations(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "setup")
	repository, err := setuprepo.New(root)
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.mkv", 1234)
	require.NoError(t, err)
	first, err := domain.NewSetupConfiguration(candidate, "First", 2026)
	require.NoError(t, err)
	second, err := domain.NewSetupConfiguration(candidate, "Second", 2026)
	require.NoError(t, err)

	require.NoError(t, repository.Save(context.Background(), "first-private-token", first))
	require.NoError(t, repository.Save(context.Background(), "second-private-token", second))

	entries, err := os.ReadDir(filepath.Join(root, "generations"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	token, loaded, err := repository.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "second-private-token", token)
	require.Equal(t, second, loaded)
}

func TestRepositoryNewRemovesOrphanGenerationWhenUnconfigured(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "setup")
	orphan := filepath.Join(root, "generations", "0123456789abcdef0123456789abcdef")
	require.NoError(t, os.MkdirAll(orphan, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "torbox.token"), []byte("orphan-secret\n"), 0o600))

	_, err := setuprepo.New(root)

	require.NoError(t, err)
	_, err = os.Stat(orphan)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRepositoryLoadReturnsNotFoundWhenUnconfigured(t *testing.T) {
	t.Parallel()
	repository, err := setuprepo.New(filepath.Join(t.TempDir(), "setup"))
	require.NoError(t, err)

	_, _, err = repository.Load(context.Background())

	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestRepositoryRejectsUnsafeTokenWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	repository, err := setuprepo.New(filepath.Join(t.TempDir(), "setup"))
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("17:3", "Example.mp4", 12)
	require.NoError(t, err)
	configuration, err := domain.NewSetupConfiguration(candidate, "Example", 2026)
	require.NoError(t, err)
	secret := " private-token "

	err = repository.Save(context.Background(), secret, configuration)

	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	_, _, loadErr := repository.Load(context.Background())
	require.True(t, errors.Is(loadErr, domain.ErrNotFound))
}
