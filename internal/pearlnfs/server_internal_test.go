package pearlnfs

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
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
		handles: make(map[[32]byte]stableHandleEntry),
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
