package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedFilesystemServesLargeLogicalFileByOffset(t *testing.T) {
	t.Parallel()
	const logicalSize = int64(1 << 40)
	filesystem := newGeneratedFilesystem(logicalSize)
	info, err := filesystem.Stat("logical-1TiB.bin")
	require.NoError(t, err)
	require.Equal(t, logicalSize, info.Size())
	file, err := filesystem.Open("logical-1TiB.bin")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	buffer := make([]byte, 4)

	count, err := file.ReadAt(buffer, logicalSize-4)

	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{252, 253, 254, 255}, buffer)
}

func TestGeneratedFilesystemReportsEOFWithoutAllocatingCompleteFile(t *testing.T) {
	t.Parallel()
	filesystem := newGeneratedFilesystem(16)
	file, err := filesystem.Open("logical-1TiB.bin")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	buffer := make([]byte, 8)

	count, err := file.ReadAt(buffer, 12)

	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, count)
	require.Equal(t, []byte{12, 13, 14, 15}, buffer[:count])
}
