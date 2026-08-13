package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/pearlfs"
	"github.com/blackpearl-media/blackpearl/internal/state"
	"github.com/stretchr/testify/require"
)

func TestRunImportsPOCAndUnmountsOnCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	mounted := make(chan struct{})
	server := &fakeMountServer{}
	deps := dependencies{
		mount: func(_ context.Context, _ string, filesystem *pearlfs.Root) (mountServer, error) {
			require.NotNil(t, filesystem)
			close(mounted)
			return server, nil
		},
		listen: net.Listen,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)

	go func() {
		result <- run(ctx, cfg, testLogger(), deps)
	}()
	select {
	case <-mounted:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not reach mount")
	}
	cancel()
	require.NoError(t, <-result)
	require.True(t, server.unmounted)

	repository, err := state.Open(context.Background(), cfg.DBPath)
	require.NoError(t, err)
	media, err := repository.List(context.Background())
	require.NoError(t, err)
	require.Len(t, media, 1)
	require.Equal(t, domain.MediaID("blackpearl-poc-2026"), media[0].ID)
	require.NoError(t, repository.Close())
}

func TestRunCleansDatabaseWhenMountFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	deps := dependencies{
		mount: func(context.Context, string, *pearlfs.Root) (mountServer, error) {
			return nil, errors.New("fuse unavailable")
		},
		listen: net.Listen,
	}

	err := run(context.Background(), cfg, testLogger(), deps)

	require.ErrorContains(t, err, "mount PearlFS")
	repository, reopenErr := state.Open(context.Background(), cfg.DBPath)
	require.NoError(t, reopenErr)
	require.NoError(t, repository.Close())
}

func testConfig(root string, source string) config.Config {
	return config.Config{
		DataDir:     filepath.Join(root, "data"),
		DBPath:      filepath.Join(root, "data", "blackpearl.db"),
		CacheDir:    filepath.Join(root, "data", "cache"),
		MountPath:   filepath.Join(root, "mount"),
		POCSource:   source,
		HTTPAddr:    "127.0.0.1:0",
		LogLevel:    "debug",
		StorageMode: domain.StorageModePersistent,
	}
}

func TestRunRejectsRollingModeBeforeCreatingRuntimePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 40 * 1024 * 1024 * 1024

	err := run(context.Background(), cfg, testLogger(), defaultDependencies())

	require.ErrorIs(t, err, domain.ErrNotConfigured)
	_, statErr := os.Stat(cfg.DataDir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}

type fakeMountServer struct {
	unmounted bool
}

func (f *fakeMountServer) Unmount() error {
	f.unmounted = true
	return nil
}

func (f *fakeMountServer) Wait() {}
