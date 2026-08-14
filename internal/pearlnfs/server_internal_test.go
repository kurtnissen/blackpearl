package pearlnfs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestStableHandleResolvesCurrentFileAfterServerRecreation(t *testing.T) {
	t.Parallel()
	catalog := stableCatalog(t)
	firstReloadable, err := NewReloadable(context.Background(), catalog)
	require.NoError(t, err)
	first := firstReloadable.(*filesystem)
	firstHandles := newStableHandleHandler(first)
	issued := firstHandles.ToHandle(first, strings.Split(catalog.media.VirtualPath, "/"))

	secondReloadable, err := NewReloadable(context.Background(), catalog)
	require.NoError(t, err)
	second := secondReloadable.(*filesystem)
	secondHandles := newStableHandleHandler(second)
	secondHandles.registerCurrent()
	resolvedFilesystem, resolvedPath, err := secondHandles.FromHandle(issued)
	require.NoError(t, err)
	file, err := resolvedFilesystem.Open(resolvedFilesystem.Join(resolvedPath...))
	require.NoError(t, err)
	content := make([]byte, 4)
	count, err := file.ReadAt(content, 0)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, "ZZZZ", string(content))
}

func newStableHandleHandler(filesystem *filesystem) *stableHandleHandler {
	return &stableHandleHandler{
		live: filesystem, snapshotter: filesystem,
		handles: make(map[[sha256.Size]byte]stableHandleEntry),
	}
}

type stableTestCatalog struct {
	media domain.Media
}

func stableCatalog(t *testing.T) *stableTestCatalog {
	t.Helper()
	backing, err := domain.NewBackingRef("generated", "stable-object")
	require.NoError(t, err)
	media, err := domain.NewMovie("stable", "BlackPearl POC", 2026, ".mp4", 4, backing)
	require.NoError(t, err)
	return &stableTestCatalog{media: media}
}

func (c *stableTestCatalog) List(context.Context) ([]domain.Media, error) {
	return []domain.Media{c.media}, nil
}

func (*stableTestCatalog) Open(context.Context, string) (domain.ReadHandle, error) {
	return &stableReadHandle{}, nil
}

type stableReadHandle struct{}

func (*stableReadHandle) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if offset >= 4 {
		return 0, io.EOF
	}
	count := min(len(destination), 4-int(offset))
	for index := range count {
		destination[index] = 'Z'
	}
	if count < len(destination) || int(offset)+count == 4 {
		return count, io.EOF
	}
	return count, nil
}

func (*stableReadHandle) Size() int64  { return 4 }
func (*stableReadHandle) Close() error { return nil }

func TestStableHandleHandlerCapsEntriesAndRetainsRecentlyUsedHandle(t *testing.T) {
	t.Parallel()
	filesystem := memfs.New()
	handler := &stableHandleHandler{
		live: filesystem, snapshotter: staticHandleSnapshotter{filesystem: filesystem},
		handles: make(map[[sha256.Size]byte]stableHandleEntry),
	}
	oldest := handler.ToHandle(filesystem, []string{"oldest"})
	secondOldest := handler.ToHandle(filesystem, []string{"second-oldest"})
	for index := 2; index < handleCacheSize; index++ {
		handler.ToHandle(filesystem, []string{fmt.Sprintf("path-%d", index)})
	}
	_, _, err := handler.FromHandle(oldest)
	require.NoError(t, err)

	handler.ToHandle(filesystem, []string{"overflow"})

	require.Len(t, handler.handles, handleCacheSize)
	_, _, err = handler.FromHandle(oldest)
	require.NoError(t, err)
	_, _, err = handler.FromHandle(secondOldest)
	require.Error(t, err)
}

type staticHandleSnapshotter struct{ filesystem billy.Filesystem }

func (s staticHandleSnapshotter) handleSnapshot(filename string) (billy.Filesystem, string) {
	return s.filesystem, filename
}

func (staticHandleSnapshotter) handlePaths() []string { return nil }
