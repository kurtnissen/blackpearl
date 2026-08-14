package acquisition_test

import (
	"crypto/sha1" // #nosec G505 -- BitTorrent v1 content identity is defined as SHA-1 of the bencoded info dictionary.
	"encoding/hex"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/stretchr/testify/require"
)

func TestNewTorrentFileInputVerifiesRawBencodedInfoHashAndCopiesBytes(t *testing.T) {
	t.Parallel()
	info := []byte("d4:name9:movie.mp46:lengthi12345ee")
	payload := append([]byte("d8:announce20:https://tracker.test4:info"), info...)
	payload = append(payload, 'e')
	sum := sha1.Sum(info) // #nosec G401 -- required BitTorrent v1 info-hash fixture.
	infoHash := hex.EncodeToString(sum[:])

	input, err := acquisition.NewTorrentFileInput(infoHash, payload)

	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputFile, input.Kind())
	require.Equal(t, infoHash, input.InfoHash())
	require.Equal(t, payload, input.File())
	require.Empty(t, input.Magnet())
	payload[0] = 'x'
	require.Equal(t, byte('d'), input.File()[0])
	copyOfFile := input.File()
	copyOfFile[0] = 'x'
	require.Equal(t, byte('d'), input.File()[0])
}

func TestNewTorrentFileInputRejectsMalformedOversizedAndMismatchedPayloads(t *testing.T) {
	t.Parallel()
	info := []byte("d4:name9:movie.mp46:lengthi12345ee")
	payload := append([]byte("d4:info"), info...)
	payload = append(payload, 'e')
	sum := sha1.Sum(info) // #nosec G401 -- required BitTorrent v1 info-hash fixture.
	infoHash := hex.EncodeToString(sum[:])

	for _, test := range []struct {
		name    string
		hash    string
		payload []byte
	}{
		{name: "empty", hash: infoHash},
		{name: "mismatched", hash: "0123456789abcdef0123456789abcdef01234567", payload: payload},
		{name: "missing info", hash: infoHash, payload: []byte("d4:name5:moviee")},
		{name: "trailing data", hash: infoHash, payload: append(append([]byte(nil), payload...), 'x')},
		{name: "malformed", hash: infoHash, payload: []byte("d4:infoi1e")},
		{name: "oversized", hash: infoHash, payload: make([]byte, acquisition.MaximumTorrentFileBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := acquisition.NewTorrentFileInput(test.hash, test.payload)
			require.Error(t, err)
		})
	}
}

func TestNewMagnetTorrentInputRequiresMatchingInfoHash(t *testing.T) {
	t.Parallel()
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	input, err := acquisition.NewMagnetTorrentInput(infoHash, "magnet:?xt=urn:btih:"+infoHash+"&tr=https%3A%2F%2Ftracker.test")
	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputMagnet, input.Kind())
	require.Equal(t, infoHash, input.InfoHash())
	require.Contains(t, input.Magnet(), "urn:btih:")
	require.Empty(t, input.File())

	_, err = acquisition.NewMagnetTorrentInput(infoHash, "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff")
	require.Error(t, err)
	_, err = acquisition.NewMagnetTorrentInput(infoHash, "https://example.test/file.torrent")
	require.Error(t, err)
}
