package torbox

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayCachedTorrentsPostsBoundedHashBatchAndPreservesRankOrder(t *testing.T) {
	t.Parallel()
	first := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	second := mustTorrentRelease(t, "second", "abcdef0123456789abcdef0123456789abcdef01")
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/v1/api/torrents/checkcached", request.URL.Path)
		require.Equal(t, "object", request.URL.Query().Get("format"))
		require.Equal(t, "false", request.URL.Query().Get("list_files"))
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		var body struct {
			Hashes []string `json:"hashes"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, []string{first.InfoHash(), second.InfoHash()}, body.Hashes)
		writeEnvelope(writer, true, "ok", `{
			"ABCDEF0123456789ABCDEF0123456789ABCDEF01":{"name":"cached","size":20,"hash":"ABCDEF0123456789ABCDEF0123456789ABCDEF01"},
			"unrequested":{"name":"other","size":1,"hash":"ffffffffffffffffffffffffffffffffffffffff"}
		}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	cached, err := gateway.CachedTorrents(context.Background(), []acquisition.Release{first, second, first})

	require.NoError(t, err)
	require.Equal(t, []acquisition.Release{second}, cached)
}

func TestGatewayCachedTorrentsSkipsNonTorrentAndHashlessResultsWithoutRequest(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(_ http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected cache request: %s", request.URL.Path)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	usenet, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "nzb", Title: "Example", Protocol: acquisition.ReleaseProtocolUsenet,
		Size: 10, Indexer: "authorized", DownloadURL: "https://indexer.invalid/example.nzb",
	})
	require.NoError(t, err)
	hashless, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "magnet", Title: "Example", Protocol: acquisition.ReleaseProtocolTorrent,
		Size: 10, Indexer: "authorized", MagnetURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)

	cached, err := gateway.CachedTorrents(context.Background(), []acquisition.Release{usenet, hashless})

	require.NoError(t, err)
	require.Empty(t, cached)
}

func TestGatewayCachedTorrentsPreservesAuthenticationAndCancellation(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "denied", http.StatusForbidden)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		_, err := gateway.CachedTorrents(context.Background(), []acquisition.Release{release})

		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, true, "ok", `{}`)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := gateway.CachedTorrents(ctx, []acquisition.Release{release})

		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestGatewayCachedTorrentsBoundsAndSanitizesProviderResponse(t *testing.T) {
	t.Parallel()
	release := mustTorrentRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")

	t.Run("provider detail", func(t *testing.T) {
		t.Parallel()
		secret := "cache-secret"
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, false, "denied "+secret+" https://signed.invalid/file", `null`)
		})
		gateway, err := New(Options{APIBaseURL: api.URL + "/v1/api/", APIToken: secret, MetadataTTL: 1, LinkTTL: 1}, api.Client())
		require.NoError(t, err)

		_, err = gateway.CachedTorrents(context.Background(), []acquisition.Release{release})

		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
		require.NotContains(t, err.Error(), "signed.invalid")
	})

	t.Run("oversized body", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`{"success":true,"detail":"` + strings.Repeat("x", maximumDiscoveryResponseBody) + `","data":{}}`))
			require.NoError(t, err)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		_, err := gateway.CachedTorrents(context.Background(), []acquisition.Release{release})

		require.Error(t, err)
	})
}

func mustTorrentRelease(t *testing.T, sourceID string, infoHash string) acquisition.Release {
	t.Helper()
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: sourceID, Title: "Example.2026", Protocol: acquisition.ReleaseProtocolTorrent,
		Size: 20, Indexer: "authorized-indexer", InfoHash: infoHash,
	})
	require.NoError(t, err)
	return release
}
