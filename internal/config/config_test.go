package config_test

import (
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestParseUsesIsolatedContainerDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{})

	require.NoError(t, err)
	require.Equal(t, "/var/lib/blackpearl", cfg.DataDir)
	require.Equal(t, "/var/lib/blackpearl/blackpearl.db", cfg.DBPath)
	require.Equal(t, "/var/lib/blackpearl/cache", cfg.CacheDir)
	require.Equal(t, "/mnt/blackpearl", cfg.MountPath)
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, domain.StorageModePersistent, cfg.StorageMode)
	require.Zero(t, cfg.CacheMaxBytes)
	require.False(t, cfg.Plex.Enabled())
	require.Equal(t, "fuse", cfg.FilesystemMode)
	require.Equal(t, ":2049", cfg.NFSAddr)
}

func TestParseAcceptsNFSFilesystemMode(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_FILESYSTEM_MODE": "nfs",
		"BLACKPEARL_NFS_ADDR":        "0.0.0.0:2049",
	})

	require.NoError(t, err)
	require.Equal(t, "nfs", cfg.FilesystemMode)
	require.Equal(t, "0.0.0.0:2049", cfg.NFSAddr)
}

func TestParseRejectsInvalidFilesystemConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		environment map[string]string
		message     string
	}{
		{
			name:        "unsupported mode",
			environment: map[string]string{"BLACKPEARL_FILESYSTEM_MODE": "webdav"},
			message:     "FILESYSTEM_MODE",
		},
		{
			name: "malformed NFS address",
			environment: map[string]string{
				"BLACKPEARL_FILESYSTEM_MODE": "nfs",
				"BLACKPEARL_NFS_ADDR":        "2049",
			},
			message: "NFS_ADDR",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Parse(test.environment)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestParseAcceptsRollingModeOnlyWithPositiveQuota(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":    "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES": "42949672960",
	})

	require.NoError(t, err)
	require.Equal(t, domain.StorageModeRolling, cfg.StorageMode)
	require.Equal(t, int64(42_949_672_960), cfg.CacheMaxBytes)
}

func TestParseRejectsRollingModeWithoutPositiveQuota(t *testing.T) {
	t.Parallel()

	_, err := config.Parse(map[string]string{"BLACKPEARL_STORAGE_MODE": "rolling"})

	require.ErrorContains(t, err, "CACHE_MAX_BYTES")
}

func TestParseRejectsRelativeStoragePaths(t *testing.T) {
	t.Parallel()

	variables := []string{
		"BLACKPEARL_DATA_DIR",
		"BLACKPEARL_DB_PATH",
		"BLACKPEARL_CACHE_DIR",
		"BLACKPEARL_MOUNT_PATH",
		"BLACKPEARL_POC_SOURCE",
	}

	for _, variable := range variables {
		t.Run(variable, func(t *testing.T) {
			t.Parallel()

			_, err := config.Parse(map[string]string{variable: "relative/path"})

			require.ErrorContains(t, err, variable)
		})
	}
}

func TestParseRequiresCompletePlexConfiguration(t *testing.T) {
	t.Parallel()

	tests := []map[string]string{
		{"BLACKPEARL_PLEX_URL": "http://plex:32400"},
		{"BLACKPEARL_PLEX_TOKEN": "secret"},
		{"BLACKPEARL_PLEX_SECTION_ID": "1"},
		{
			"BLACKPEARL_PLEX_URL":   "http://plex:32400",
			"BLACKPEARL_PLEX_TOKEN": "secret",
		},
	}

	for index, environment := range tests {
		_, err := config.Parse(environment)

		require.ErrorContains(t, err, "Plex configuration", "case %d", index)
	}
}

func TestParseAcceptsCompletePlexConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_PLEX_URL":        "http://plex:32400",
		"BLACKPEARL_PLEX_TOKEN":      "secret",
		"BLACKPEARL_PLEX_SECTION_ID": "7",
	})

	require.NoError(t, err)
	require.True(t, cfg.Plex.Enabled())
	require.Equal(t, "7", cfg.Plex.SectionID)
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BLACKPEARL_DATA_DIR", root)
	t.Setenv("BLACKPEARL_DB_PATH", root+"/blackpearl.db")
	t.Setenv("BLACKPEARL_CACHE_DIR", root+"/cache")
	t.Setenv("BLACKPEARL_MOUNT_PATH", root+"/mount")
	t.Setenv("BLACKPEARL_STORAGE_MODE", "persistent")
	t.Setenv("BLACKPEARL_CACHE_MAX_BYTES", "0")
	t.Setenv("BLACKPEARL_POC_SOURCE", "")
	t.Setenv("BLACKPEARL_PLEX_URL", "")
	t.Setenv("BLACKPEARL_PLEX_TOKEN", "")
	t.Setenv("BLACKPEARL_PLEX_SECTION_ID", "")

	cfg, err := config.Load()

	require.NoError(t, err)
	require.Equal(t, root, cfg.DataDir)
}

func TestLoadReportsInvalidProcessEnvironment(t *testing.T) {
	t.Setenv("BLACKPEARL_STORAGE_MODE", "invalid")

	_, err := config.Load()

	require.ErrorContains(t, err, "STORAGE_MODE")
}
