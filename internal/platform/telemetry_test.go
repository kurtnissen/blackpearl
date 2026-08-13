package platform_test

import (
	"context"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestInitTelemetryCreatesShutdownWithoutExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	shutdown, err := platform.InitTelemetry(context.Background(), "blackpearl-test")

	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}

func TestInitTelemetryRequiresServiceName(t *testing.T) {
	t.Parallel()

	_, err := platform.InitTelemetry(context.Background(), "")

	require.ErrorContains(t, err, "service name is required")
}
