package torbox

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayCreateCachedTorrentUsesAuthoritativeCachedOnlyGuard(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/v1/api/torrents/createtorrent", request.URL.Path)
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		require.NoError(t, request.ParseMultipartForm(1<<20))
		require.Equal(t, "magnet:?xt=urn%3Abtih%3A0123456789abcdef0123456789abcdef01234567", request.FormValue("magnet"))
		require.Equal(t, "3", request.FormValue("seed"))
		require.Equal(t, "false", request.FormValue("allow_zip"))
		require.Equal(t, "false", request.FormValue("as_queued"))
		require.Equal(t, "true", request.FormValue("add_only_if_cached"))
		writeEnvelope(writer, true, "added", `{"hash":"0123456789ABCDEF0123456789ABCDEF01234567","torrent_id":17,"auth_id":"redacted"}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	created, err := gateway.CreateCachedTorrent(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, "torbox-torrent", created.Provider())
	require.Equal(t, "17", created.ObjectID())
}

func TestGatewayCreateCachedTorrentRejectsIneligibleReleaseWithoutRequest(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(_ http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected create request: %s", request.URL.Path)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	usenet, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "nzb", Title: "Example", Protocol: acquisition.ReleaseProtocolUsenet,
		Size: 10, Indexer: "authorized", DownloadURL: "https://indexer.invalid/example.nzb",
	})
	require.NoError(t, err)

	_, err = gateway.CreateCachedTorrent(context.Background(), usenet)

	require.Error(t, err)
}

func TestGatewayCreateCachedTorrentRejectsInvalidProviderResult(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	tests := []struct {
		name string
		data string
	}{
		{name: "zero ID", data: `{"hash":"0123456789abcdef0123456789abcdef01234567","torrent_id":0}`},
		{name: "mismatched hash", data: `{"hash":"ffffffffffffffffffffffffffffffffffffffff","torrent_id":17}`},
		{name: "missing hash", data: `{"torrent_id":17}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
				writeEnvelope(writer, true, "added", test.data)
			})
			gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

			_, err := gateway.CreateCachedTorrent(context.Background(), release)

			require.Error(t, err)
		})
	}
}

func TestGatewayCreateCachedTorrentDoesNotRetryAmbiguousFailure(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	var requests atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("ambiguous upstream failure with test-token")
	})}
	gateway := newTestGateway(t, "https://api.invalid/v1/api/", client)

	_, err := gateway.CreateCachedTorrent(context.Background(), release)

	require.Error(t, err)
	require.Equal(t, int64(1), requests.Load())
	require.NotContains(t, err.Error(), "test-token")
	require.NotContains(t, err.Error(), "api.invalid")
}

func TestGatewayCreateCachedTorrentPreservesAuthenticationAndCancellation(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "denied", http.StatusUnauthorized)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		_, err := gateway.CreateCachedTorrent(context.Background(), release)

		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, true, "added", `{"hash":"0123456789abcdef0123456789abcdef01234567","torrent_id":17}`)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := gateway.CreateCachedTorrent(ctx, release)

		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestGatewayCreateCachedTorrentSanitizesProviderDetail(t *testing.T) {
	t.Parallel()
	secret := "create-secret"
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeEnvelope(writer, false, "denied "+secret+" https://signed.invalid/file", `null`)
	})
	gateway, err := New(Options{APIBaseURL: api.URL + "/v1/api/", APIToken: secret, MetadataTTL: 1, LinkTTL: 1}, api.Client())
	require.NoError(t, err)

	_, err = gateway.CreateCachedTorrent(context.Background(), release)

	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, err.Error(), "signed.invalid")
}
