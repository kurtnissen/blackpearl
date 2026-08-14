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
	for _, name := range []string{"torbox.token", "configuration.json"} {
		info, statErr := os.Stat(filepath.Join(root, name))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
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
