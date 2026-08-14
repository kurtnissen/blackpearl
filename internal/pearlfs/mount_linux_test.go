//go:build linux

package pearlfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMountedFilesystemServesExactBytesAndOffsetReads(t *testing.T) {
	if os.Getenv("BLACKPEARL_FUSE_TEST") != "1" {
		t.Skip("set BLACKPEARL_FUSE_TEST=1 to run the kernel-mounted FUSE test")
	}
	content := []byte("blackpearl-mounted-test-video-bytes")
	media := mustMovie(t, "poc", "BlackPearl POC", int64(len(content)), "key")
	catalog := &fakeCatalog{
		media:   []domain.Media{media},
		content: map[string][]byte{media.VirtualPath: content},
	}
	root, err := New(context.Background(), catalog)
	require.NoError(t, err)
	mountPath := t.TempDir()
	server, err := Mount(context.Background(), mountPath, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Unmount()) })
	virtualFile := filepath.Join(mountPath, filepath.FromSlash(media.VirtualPath))

	actual, err := os.ReadFile(virtualFile)
	require.NoError(t, err)
	require.Equal(t, content, actual)

	file, err := os.Open(virtualFile)
	require.NoError(t, err)
	buffer := make([]byte, 7)
	count, err := file.ReadAt(buffer, 11)
	require.NoError(t, err)
	require.Equal(t, 7, count)
	require.Equal(t, []byte("mounted"), buffer)
	require.NoError(t, file.Close())
}
