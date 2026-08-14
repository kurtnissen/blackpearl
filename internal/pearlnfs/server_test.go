package pearlnfs_test

import (
	"context"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/pearlnfs"
	"github.com/stretchr/testify/require"
)

func TestServerRejectsCanceledContextBeforeListening(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	filesystem := newTestFilesystem(t, context.Background())

	_, err := pearlnfs.Start(ctx, "127.0.0.1:0", filesystem)

	require.ErrorIs(t, err, context.Canceled)
}

func TestServerListensAndStopsCleanly(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())
	server, err := pearlnfs.Start(context.Background(), "127.0.0.1:0", filesystem)
	require.NoError(t, err)
	require.NotNil(t, server.Addr())
	require.NotZero(t, server.Addr().String())

	require.NoError(t, server.Close())
	require.NoError(t, server.Wait())
}

func TestServerRejectsNilFilesystemAndUnavailableAddress(t *testing.T) {
	t.Parallel()
	filesystem := newTestFilesystem(t, context.Background())

	_, err := pearlnfs.Start(context.Background(), "127.0.0.1:0", nil)
	require.ErrorContains(t, err, "filesystem is required")
	_, err = pearlnfs.Start(context.Background(), "not-a-listen-address", filesystem)
	require.ErrorContains(t, err, "listen for PearlNFS")
}

func TestServerReloadsReloadableFilesystem(t *testing.T) {
	t.Parallel()
	filesystem, err := pearlnfs.NewReloadable(context.Background(), catalogStub{})
	require.NoError(t, err)
	server, err := pearlnfs.Start(context.Background(), "127.0.0.1:0", filesystem)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, server.Wait())
	})

	require.NoError(t, server.Reload(context.Background()))
}
