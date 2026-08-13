package torbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseObjectIDReturnsCanonicalIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input     string
		torrentID int64
		fileID    int64
	}{
		{input: "1:2", torrentID: 1, fileID: 2},
		{input: "9223372036854775807:9", torrentID: 9223372036854775807, fileID: 9},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()

			got, err := parseObjectID(test.input)

			require.NoError(t, err)
			require.Equal(t, test.torrentID, got.TorrentID)
			require.Equal(t, test.fileID, got.FileID)
			require.Equal(t, test.input, got.String())
		})
	}
}

func TestParseObjectIDRejectsNoncanonicalIDs(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"", "0:1", "1:0", "-1:2", "1:-2", " 1:2", "1:2 ", "01:2", "1:02",
		"1", "1:2:3", "a:2", "1:b", "9223372036854775808:1", "1:9223372036854775808",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			_, err := parseObjectID(input)

			require.Error(t, err)
		})
	}
}
