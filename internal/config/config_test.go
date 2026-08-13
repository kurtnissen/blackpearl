package config_test

import (
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/config"
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
	require.False(t, cfg.Plex.Enabled())
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
