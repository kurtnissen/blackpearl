package config_test

import (
	"testing"
	"time"

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
	require.Equal(t, int64(262_144), cfg.CacheChunkBytes)
	require.Equal(t, "http-range", cfg.RangeProvider)
	require.Equal(t, "https://api.torbox.app/v1/api/", cfg.TorBoxAPIURL)
	require.Equal(t, 30*time.Second, cfg.RangeTimeout)
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

func TestParseAcceptsCompleteRollingRangeConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":      "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES":   "42949672960",
		"BLACKPEARL_CACHE_CHUNK_BYTES": "262144",
		"BLACKPEARL_RANGE_ORIGIN_URL":  "http://range-origin/media/",
		"BLACKPEARL_RANGE_OBJECT_ID":   "blackpearl-poc.mp4",
		"BLACKPEARL_RANGE_TIMEOUT":     "30s",
	})

	require.NoError(t, err)
	require.Equal(t, domain.StorageModeRolling, cfg.StorageMode)
	require.Equal(t, int64(42_949_672_960), cfg.CacheMaxBytes)
	require.Equal(t, int64(262_144), cfg.CacheChunkBytes)
	require.Equal(t, "http://range-origin/media/", cfg.RangeOriginURL)
	require.Equal(t, "blackpearl-poc.mp4", cfg.RangeObjectID)
	require.Equal(t, 30*time.Second, cfg.RangeTimeout)
}

func TestParseAcceptsCompleteRollingTorBoxConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":     "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES":  "42949672960",
		"BLACKPEARL_RANGE_PROVIDER":   "torbox-torrent",
		"BLACKPEARL_RANGE_OBJECT_ID":  "17:3",
		"BLACKPEARL_TORBOX_API_URL":   "https://api.torbox.app/v1/api/",
		"BLACKPEARL_TORBOX_API_TOKEN": "secret-token",
	})

	require.NoError(t, err)
	require.Equal(t, "torbox-torrent", cfg.RangeProvider)
	require.Equal(t, "17:3", cfg.RangeObjectID)
	require.Equal(t, "secret-token", cfg.TorBoxAPIToken)
	require.Empty(t, cfg.RangeOriginURL)
}

func TestParseAcceptsRollingTorBoxSecretFile(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":          "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES":       "42949672960",
		"BLACKPEARL_RANGE_PROVIDER":        "torbox-torrent",
		"BLACKPEARL_RANGE_OBJECT_ID":       "17:3",
		"BLACKPEARL_TORBOX_API_TOKEN_FILE": "/run/secrets/torbox_api_token",
	})

	require.NoError(t, err)
	require.Empty(t, cfg.TorBoxAPIToken)
	require.Equal(t, "/run/secrets/torbox_api_token", cfg.TorBoxAPITokenFile)
}

func TestParseAcceptsBrowserSetupModeWithoutCredentialOrSelection(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":    "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES": "1048576",
		"BLACKPEARL_RANGE_PROVIDER":  "torbox-torrent",
		"BLACKPEARL_SETUP_ENABLED":   "true",
		"BLACKPEARL_SETUP_DIR":       "/private/setup",
		"BLACKPEARL_FILESYSTEM_MODE": "nfs",
	})

	require.NoError(t, err)
	require.True(t, cfg.SetupEnabled)
	require.Equal(t, "/private/setup", cfg.SetupDir)
	require.Empty(t, cfg.RangeObjectID)
	require.Empty(t, cfg.TorBoxAPIToken)
}

func TestParseRejectsBrowserSetupOutsideRollingTorBoxNFS(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"BLACKPEARL_STORAGE_MODE":    "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES": "1048576",
		"BLACKPEARL_RANGE_PROVIDER":  "torbox-torrent",
		"BLACKPEARL_SETUP_ENABLED":   "true",
		"BLACKPEARL_FILESYSTEM_MODE": "nfs",
	}
	tests := []struct{ name, key, value string }{
		{name: "persistent", key: "BLACKPEARL_STORAGE_MODE", value: "persistent"},
		{name: "HTTP range", key: "BLACKPEARL_RANGE_PROVIDER", value: "http-range"},
		{name: "FUSE", key: "BLACKPEARL_FILESYSTEM_MODE", value: "fuse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := make(map[string]string, len(base))
			for key, value := range base {
				environment[key] = value
			}
			environment[test.key] = test.value

			_, err := config.Parse(environment)

			require.ErrorContains(t, err, "SETUP_ENABLED")
		})
	}
}

func TestParseRejectsInvalidTorBoxSecretFileConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		environment map[string]string
		message     string
	}{
		{
			name: "inline and file token",
			environment: map[string]string{
				"BLACKPEARL_STORAGE_MODE":          "rolling",
				"BLACKPEARL_CACHE_MAX_BYTES":       "1048576",
				"BLACKPEARL_RANGE_PROVIDER":        "torbox-torrent",
				"BLACKPEARL_RANGE_OBJECT_ID":       "17:3",
				"BLACKPEARL_TORBOX_API_TOKEN":      "secret-token",
				"BLACKPEARL_TORBOX_API_TOKEN_FILE": "/run/secrets/torbox_api_token",
			},
			message: "exactly one",
		},
		{
			name: "relative file",
			environment: map[string]string{
				"BLACKPEARL_STORAGE_MODE":          "rolling",
				"BLACKPEARL_CACHE_MAX_BYTES":       "1048576",
				"BLACKPEARL_RANGE_PROVIDER":        "torbox-torrent",
				"BLACKPEARL_RANGE_OBJECT_ID":       "17:3",
				"BLACKPEARL_TORBOX_API_TOKEN_FILE": "secrets/torbox_api_token",
			},
			message: "absolute path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Parse(test.environment)

			require.ErrorContains(t, err, test.message)
			require.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestParseRejectsInvalidRollingTorBoxConfiguration(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"BLACKPEARL_STORAGE_MODE":     "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES":  "1048576",
		"BLACKPEARL_RANGE_PROVIDER":   "torbox-torrent",
		"BLACKPEARL_RANGE_OBJECT_ID":  "17:3",
		"BLACKPEARL_TORBOX_API_URL":   "https://api.torbox.app/v1/api/",
		"BLACKPEARL_TORBOX_API_TOKEN": "secret-token",
	}
	tests := []struct{ name, variable, value, message string }{
		{name: "missing token", variable: "BLACKPEARL_TORBOX_API_TOKEN", value: "", message: "TORBOX_API_TOKEN"},
		{name: "insecure API URL", variable: "BLACKPEARL_TORBOX_API_URL", value: "http://api.torbox.app/v1/api/", message: "TORBOX_API_URL"},
		{name: "noncanonical object", variable: "BLACKPEARL_RANGE_OBJECT_ID", value: "017:3", message: "RANGE_OBJECT_ID"},
		{name: "HTTP origin mixed in", variable: "BLACKPEARL_RANGE_ORIGIN_URL", value: "https://origin.invalid/", message: "RANGE_ORIGIN_URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := make(map[string]string, len(valid)+1)
			for key, value := range valid {
				environment[key] = value
			}
			environment[test.variable] = test.value

			_, err := config.Parse(environment)

			require.ErrorContains(t, err, test.message)
			require.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestParseRejectsTorBoxTokenInPersistentMode(t *testing.T) {
	t.Parallel()

	_, err := config.Parse(map[string]string{
		"BLACKPEARL_STORAGE_MODE":     "persistent",
		"BLACKPEARL_TORBOX_API_TOKEN": "secret-token",
	})

	require.ErrorContains(t, err, "TORBOX_API_TOKEN")
	require.NotContains(t, err.Error(), "secret-token")
}

func TestParseRejectsRollingModeWithoutPositiveQuota(t *testing.T) {
	t.Parallel()

	_, err := config.Parse(map[string]string{"BLACKPEARL_STORAGE_MODE": "rolling"})

	require.ErrorContains(t, err, "CACHE_MAX_BYTES")
}

func TestParseRejectsInvalidRollingRangeConfiguration(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"BLACKPEARL_STORAGE_MODE":      "rolling",
		"BLACKPEARL_CACHE_MAX_BYTES":   "1048576",
		"BLACKPEARL_CACHE_CHUNK_BYTES": "262144",
		"BLACKPEARL_RANGE_ORIGIN_URL":  "http://range-origin/media/",
		"BLACKPEARL_RANGE_OBJECT_ID":   "blackpearl-poc.mp4",
		"BLACKPEARL_RANGE_TIMEOUT":     "30s",
	}
	tests := []struct {
		name     string
		variable string
		value    string
		message  string
	}{
		{name: "missing origin URL", variable: "BLACKPEARL_RANGE_ORIGIN_URL", value: "", message: "RANGE_ORIGIN_URL"},
		{name: "missing object ID", variable: "BLACKPEARL_RANGE_OBJECT_ID", value: "", message: "RANGE_OBJECT_ID"},
		{name: "non HTTP origin", variable: "BLACKPEARL_RANGE_ORIGIN_URL", value: "ftp://range-origin/media/", message: "RANGE_ORIGIN_URL"},
		{name: "zero chunk size", variable: "BLACKPEARL_CACHE_CHUNK_BYTES", value: "0", message: "CACHE_CHUNK_BYTES"},
		{name: "chunk larger than quota", variable: "BLACKPEARL_CACHE_CHUNK_BYTES", value: "2097152", message: "CACHE_CHUNK_BYTES"},
		{name: "zero timeout", variable: "BLACKPEARL_RANGE_TIMEOUT", value: "0s", message: "RANGE_TIMEOUT"},
		{name: "local source", variable: "BLACKPEARL_POC_SOURCE", value: "/fixture/full.mp4", message: "POC_SOURCE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := make(map[string]string, len(valid)+1)
			for key, value := range valid {
				environment[key] = value
			}
			environment[test.variable] = test.value

			_, err := config.Parse(environment)

			require.ErrorContains(t, err, test.message)
		})
	}
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
