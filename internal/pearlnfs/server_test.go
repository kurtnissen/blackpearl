package pearlnfs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/pearlnfs"
	"github.com/stretchr/testify/require"
	nfsclient "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
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

func TestServerReplacementKeepsIssuedNFSFileHandleOnOriginalCatalog(t *testing.T) {
	t.Parallel()
	oldCatalog := newByteCatalog(t, 'A')
	newCatalog := newByteCatalog(t, 'B')
	filesystem, err := pearlnfs.NewReloadable(context.Background(), oldCatalog)
	require.NoError(t, err)
	server, err := pearlnfs.Start(context.Background(), "127.0.0.1:0", filesystem)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, server.Wait())
	})
	oldTarget := mountTarget(t, server.Addr().String())
	oldFile, err := oldTarget.Open(testVirtualPath)
	require.NoError(t, err)
	issuedBytes := make([]byte, 4)
	count, err := oldFile.ReadAt(issuedBytes, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, "AAAA", string(issuedBytes))

	previous, err := server.Replace(context.Background(), newCatalog)
	require.NoError(t, err)
	require.Same(t, oldCatalog, previous)
	oldBytes := make([]byte, 4)
	count, err = oldFile.ReadAt(oldBytes, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, "AAAA", string(oldBytes))

	newTarget := mountTarget(t, server.Addr().String())
	newFile, err := newTarget.Open(testVirtualPath)
	require.NoError(t, err)
	newBytes := make([]byte, 4)
	count, err = newFile.ReadAt(newBytes, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, "BBBB", string(newBytes))
}

func TestServerReplacementDoesNotRaceWithIssuedNFSFileHandleReads(t *testing.T) {
	oldCatalog := newByteCatalog(t, 'A')
	newCatalog := newByteCatalog(t, 'B')
	filesystem, err := pearlnfs.NewReloadable(context.Background(), oldCatalog)
	require.NoError(t, err)
	server, err := pearlnfs.Start(context.Background(), "127.0.0.1:0", filesystem)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, server.Wait())
	})
	target := mountTarget(t, server.Addr().String())
	oldFile, err := target.Open(testVirtualPath)
	require.NoError(t, err)
	issued := make([]byte, 4)
	_, err = oldFile.ReadAt(issued, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, "AAAA", string(issued))

	var readers sync.WaitGroup
	readErrors := make(chan error, 4)
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 50 {
				content := make([]byte, 4)
				count, readErr := oldFile.ReadAt(content, 0)
				if !errors.Is(readErr, io.EOF) || count != 4 || string(content) != "AAAA" {
					readErrors <- fmt.Errorf("old handle read %q (%d bytes): %w", content, count, readErr)
					return
				}
			}
		}()
	}
	for index := range 20 {
		catalog := newCatalog
		if index%2 == 1 {
			catalog = oldCatalog
		}
		_, err = server.Replace(context.Background(), catalog)
		require.NoError(t, err)
	}
	readers.Wait()
	close(readErrors)
	for readErr := range readErrors {
		require.NoError(t, readErr)
	}
}

func mountTarget(t *testing.T, address string) *nfsclient.Target {
	t.Helper()
	var client *rpc.Client
	var err error
	for range 10 {
		client, err = rpc.DialTCP("tcp", address, false)
		if !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	require.NoError(t, err)
	mount := &nfsclient.Mount{Client: client}
	t.Cleanup(func() { mount.Close() })
	auth := rpc.NewAuthUnix("blackpearl-test", 1000, 1000)
	target, err := mount.Mount("/", auth.Auth())
	require.NoError(t, err)
	t.Cleanup(func() { target.Close() })
	return target
}

type byteCatalog struct {
	media domain.Media
	value byte
}

func newByteCatalog(t *testing.T, value byte) *byteCatalog {
	t.Helper()
	backing, err := domain.NewBackingRef("generated", string([]byte{value}))
	require.NoError(t, err)
	media, err := domain.NewMovie("stable", "BlackPearl POC", 2026, ".mp4", 4, backing)
	require.NoError(t, err)
	return &byteCatalog{media: media, value: value}
}

func (c *byteCatalog) List(context.Context) ([]domain.Media, error) {
	return []domain.Media{c.media}, nil
}

func (c *byteCatalog) Open(context.Context, string) (domain.ReadHandle, error) {
	return &byteHandle{value: c.value}, nil
}

type byteHandle struct{ value byte }

func (h *byteHandle) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if offset >= 4 {
		return 0, io.EOF
	}
	count := min(len(destination), 4-int(offset))
	for index := range count {
		destination[index] = h.value
	}
	if count < len(destination) {
		return count, io.EOF
	}
	return count, nil
}

func (*byteHandle) Size() int64  { return 4 }
func (*byteHandle) Close() error { return nil }
