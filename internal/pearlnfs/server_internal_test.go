package pearlnfs

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/stretchr/testify/require"
)

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
