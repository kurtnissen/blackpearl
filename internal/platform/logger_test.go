package platform_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestNewLoggerWritesStructuredJSONAtConfiguredLevel(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger, err := platform.NewLogger("warn", &output)
	require.NoError(t, err)

	logger.Info("hidden")
	logger.Warn("visible", "mediaId", "poc")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "WARN", entry["level"])
	require.Equal(t, "visible", entry["msg"])
	require.Equal(t, "poc", entry["mediaId"])
}

func TestNewLoggerRejectsUnknownLevel(t *testing.T) {
	t.Parallel()

	_, err := platform.NewLogger("loud", &bytes.Buffer{})

	require.ErrorContains(t, err, "log level")
}
