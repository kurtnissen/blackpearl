package acquisition_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionrepo "github.com/blackpearl-media/blackpearl/internal/repository/acquisition"
	"github.com/stretchr/testify/require"
)

func TestRepositorySaveSurvivesReopenWithPrivatePermissions(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "acquisition")
	repository, err := acquisitionrepo.New(root)
	require.NoError(t, err)
	settings := mustSettings(t, "http://prowlarr:9696/base/", "private-api-key")

	require.NoError(t, repository.Save(context.Background(), settings))
	reopened, err := acquisitionrepo.New(root)
	require.NoError(t, err)
	loaded, err := reopened.Load(context.Background())

	require.NoError(t, err)
	require.Equal(t, settings.Provider(), loaded.Provider())
	require.Equal(t, settings.Endpoint(), loaded.Endpoint())
	require.Equal(t, settings.Credential(), loaded.Credential())
	rootInfo, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), rootInfo.Mode().Perm())
	fileInfo, err := os.Stat(filepath.Join(root, "search-provider.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
}

func TestRepositorySaveAtomicallyReplacesPriorSettings(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "acquisition")
	repository, err := acquisitionrepo.New(root)
	require.NoError(t, err)
	first := mustSettings(t, "http://first:9696", "first-key")
	second := mustSettings(t, "https://second.example/base", "second-key")
	require.NoError(t, repository.Save(context.Background(), first))

	require.NoError(t, repository.Save(context.Background(), second))
	loaded, err := repository.Load(context.Background())

	require.NoError(t, err)
	require.Equal(t, second.Endpoint(), loaded.Endpoint())
	require.Equal(t, second.Credential(), loaded.Credential())
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRepositoryLoadReportsNotFoundAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	repository, err := acquisitionrepo.New(filepath.Join(t.TempDir(), "acquisition"))
	require.NoError(t, err)

	_, err = repository.Load(context.Background())
	require.ErrorIs(t, err, domain.ErrNotFound)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repository.Load(ctx)
	require.ErrorIs(t, err, context.Canceled)
	settings := mustSettings(t, "http://prowlarr:9696", "private-key")
	err = repository.Save(ctx, settings)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRepositoryLoadRejectsCorruptAndOversizedStateWithoutEcho(t *testing.T) {
	t.Parallel()
	secret := "repository-secret"
	tests := []struct {
		name    string
		content string
	}{
		{name: "corrupt", content: `{"provider":"prowlarr","endpoint":"http://prowlarr:9696","credential":"` + secret + `"`},
		{name: "invalid values", content: `{"provider":"prowlarr","endpoint":"relative","credential":"` + secret + `"}`},
		{name: "oversized", content: strings.Repeat("x", 20*1024)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "acquisition")
			repository, err := acquisitionrepo.New(root)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, "search-provider.json"), []byte(test.content), 0o600))

			_, err = repository.Load(context.Background())

			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), "relative")
		})
	}
}

func TestRepositoryNewRequiresAbsoluteRoot(t *testing.T) {
	t.Parallel()

	_, err := acquisitionrepo.New("relative/acquisition")

	require.Error(t, err)
}

func mustSettings(t *testing.T, endpoint string, credential string) acquisitiondomain.SearchProviderSettings {
	t.Helper()
	settings, err := acquisitiondomain.NewSearchProviderSettings("prowlarr", endpoint, credential)
	require.NoError(t, err)
	return settings
}
