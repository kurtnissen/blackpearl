package acquisition_test

import (
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/stretchr/testify/require"
)

func TestNewByteRangeAcceptsPositiveLengthAtAnyNonnegativeOffset(t *testing.T) {
	t.Parallel()

	byteRange, err := acquisition.NewByteRange(7, 3)

	require.NoError(t, err)
	require.Equal(t, int64(7), byteRange.Offset)
	require.Equal(t, int64(3), byteRange.Length)
}

func TestNewByteRangeRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		offset int64
		length int64
	}{
		{name: "negative offset", offset: -1, length: 1},
		{name: "zero length", offset: 0, length: 0},
		{name: "negative length", offset: 0, length: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := acquisition.NewByteRange(test.offset, test.length)
			require.Error(t, err)
		})
	}
}
