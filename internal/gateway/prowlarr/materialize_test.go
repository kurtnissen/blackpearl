package prowlarr_test

import (
	"context"
	"crypto/sha1" // #nosec G505 -- BitTorrent v1 fixtures require SHA-1 info hashes.
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/prowlarr"
	"github.com/stretchr/testify/require"
)

func TestMaterializeReturnsValidatedMagnetWithoutNetworkRequest(t *testing.T) {
	t.Parallel()
	const infoHash = "0123456789abcdef0123456789abcdef01234567"
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "private-key"}, server.Client())
	require.NoError(t, err)
	release := mustMaterializedRelease(t, infoHash, "magnet:?xt=urn:btih:"+infoHash, "")

	input, err := gateway.Materialize(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputMagnet, input.Kind())
	require.Zero(t, requests.Load())
}

func TestMaterializeDownloadsBoundedSameOriginTorrentAndVerifiesHash(t *testing.T) {
	t.Parallel()
	payload, infoHash := torrentPayload()
	const apiKey = "private-key"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/prowlarr/api/v1/indexer/1/download", request.URL.Path)
		require.Equal(t, apiKey, request.Header.Get("X-Api-Key"))
		writer.Header().Set("Content-Type", "application/x-bittorrent")
		_, err := writer.Write(payload)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL + "/prowlarr/", APIKey: apiKey}, server.Client())
	require.NoError(t, err)
	release := mustMaterializedRelease(t, infoHash, "", server.URL+"/prowlarr/api/v1/indexer/1/download?link=signed")

	input, err := gateway.Materialize(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputFile, input.Kind())
	require.Equal(t, payload, input.File())
}

func TestMaterializeRejectsCrossOriginRedirectOversizeMismatchAndCredentials(t *testing.T) {
	t.Parallel()
	payload, infoHash := torrentPayload()
	secret := "private-key"
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		download func(string) string
		want     error
	}{
		{name: "redirect", handler: func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "https://outside.test/file.torrent", http.StatusFound)
		}, download: func(base string) string { return base + "/download" }},
		{name: "oversize", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(make([]byte, acquisition.MaximumTorrentFileBytes+1))
		}, download: func(base string) string { return base + "/download" }},
		{name: "mismatch", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(payload)
		}, download: func(base string) string { return base + "/download" }},
		{name: "unauthorized", handler: func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, secret, http.StatusUnauthorized)
		}, download: func(base string) string { return base + "/download" }, want: domain.ErrUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: secret}, server.Client())
			require.NoError(t, err)
			hash := infoHash
			if test.name == "mismatch" {
				hash = "0123456789abcdef0123456789abcdef01234567"
			}
			release := mustMaterializedRelease(t, hash, "", test.download(server.URL))

			_, err = gateway.Materialize(context.Background(), release)

			require.Error(t, err)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			}
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), "outside.test")
		})
	}

	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: secret}, http.DefaultClient)
	require.NoError(t, err)
	crossOrigin := mustMaterializedRelease(t, infoHash, "", "https://outside.test/file.torrent")
	_, err = gateway.Materialize(context.Background(), crossOrigin)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "outside.test")
}

func torrentPayload() ([]byte, string) {
	info := []byte("d4:name9:movie.mp46:lengthi12345ee")
	payload := append([]byte("d4:info"), info...)
	payload = append(payload, 'e')
	sum := sha1.Sum(info) // #nosec G401 -- required BitTorrent v1 info-hash fixture.
	return payload, hex.EncodeToString(sum[:])
}

func mustMaterializedRelease(t *testing.T, infoHash string, magnet string, downloadURL string) acquisition.Release {
	t.Helper()
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: fmt.Sprintf("result-%s", infoHash), Title: "Example.Movie.2026",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 12345, Indexer: "authorized-indexer",
		InfoHash: infoHash, MagnetURL: magnet, DownloadURL: downloadURL,
	})
	require.NoError(t, err)
	return release
}
