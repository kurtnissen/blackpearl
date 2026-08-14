package torbox

import (
	"context"
	"crypto/sha1" // #nosec G505 -- BitTorrent v1 fixtures require SHA-1 info hashes.
	"encoding/hex"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayCreateTorrentUsesExplicitAllowDownloadPolicyAndFilePayload(t *testing.T) {
	t.Parallel()
	payload, infoHash := torboxTorrentPayload()
	input, err := acquisition.NewTorrentFileInput(infoHash, payload)
	require.NoError(t, err)
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/api/torrents/createtorrent", request.URL.Path)
		require.NoError(t, request.ParseMultipartForm(int64(acquisition.MaximumTorrentFileBytes+1024)))
		require.Equal(t, "false", request.FormValue("add_only_if_cached"))
		files := request.MultipartForm.File["file"]
		require.Len(t, files, 1)
		opened, openErr := files[0].Open()
		require.NoError(t, openErr)
		t.Cleanup(func() { require.NoError(t, opened.Close()) })
		read := make([]byte, len(payload))
		_, readErr := opened.Read(read)
		require.NoError(t, readErr)
		require.Equal(t, payload, read)
		writeEnvelope(writer, true, "added", `{"hash":"`+infoHash+`","torrent_id":17}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	created, err := gateway.CreateTorrent(context.Background(), input, true)

	require.NoError(t, err)
	require.Equal(t, "17", created.ObjectID())
}

func TestGatewayCreateTorrentDoesNotRetryAndRejectsMismatchedResponse(t *testing.T) {
	t.Parallel()
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	input, err := acquisition.NewMagnetTorrentInput(infoHash, "magnet:?xt=urn:btih:"+infoHash)
	require.NoError(t, err)
	var requests atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeEnvelope(writer, true, "added", `{"hash":"ffffffffffffffffffffffffffffffffffffffff","torrent_id":17}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	_, err = gateway.CreateTorrent(context.Background(), input, true)

	require.Error(t, err)
	require.Equal(t, int64(1), requests.Load())
}

func TestGatewayFindTorrentByHashReturnsUniqueAccountObject(t *testing.T) {
	t.Parallel()
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/api/torrents/mylist", request.URL.Path)
		require.Equal(t, "true", request.URL.Query().Get("bypass_cache"))
		writeEnvelope(writer, true, "ok", `[{"id":17,"hash":"`+infoHash+`"},{"id":18,"hash":"ffffffffffffffffffffffffffffffffffffffff"}]`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	created, err := gateway.FindTorrentByHash(context.Background(), infoHash)

	require.NoError(t, err)
	require.Equal(t, "17", created.ObjectID())
}

func TestGatewayFindTorrentByHashDistinguishesMissingAndAmbiguous(t *testing.T) {
	t.Parallel()
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	for _, test := range []struct {
		name string
		data string
		want error
	}{
		{name: "missing", data: `[]`, want: domain.ErrNotFound},
		{name: "ambiguous", data: `[{"id":17,"hash":"` + infoHash + `"},{"id":18,"hash":"` + infoHash + `"}]`, want: acquisition.ErrAmbiguousProviderObjects},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) { writeEnvelope(writer, true, "ok", test.data) })
			gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
			_, err := gateway.FindTorrentByHash(context.Background(), infoHash)
			require.ErrorIs(t, err, test.want)
		})
	}
}

func torboxTorrentPayload() ([]byte, string) {
	info := []byte("d4:name9:movie.mp46:lengthi12345ee")
	payload := append([]byte("d4:info"), info...)
	payload = append(payload, 'e')
	sum := sha1.Sum(info) // #nosec G401 -- required BitTorrent v1 info-hash fixture.
	return payload, hex.EncodeToString(sum[:])
}
