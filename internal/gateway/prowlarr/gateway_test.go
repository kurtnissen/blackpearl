package prowlarr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/prowlarr"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsInvalidConfigurationWithoutLeakingKey(t *testing.T) {
	t.Parallel()
	secret := "prowlarr-secret-key"
	validClient := &http.Client{}
	tests := []struct {
		name    string
		options prowlarr.Options
		client  *http.Client
	}{
		{name: "nil client", options: prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: secret}},
		{name: "blank key", options: prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: " "}, client: validClient},
		{name: "surrounding key whitespace", options: prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: " " + secret}, client: validClient},
		{name: "control character in key", options: prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: secret + "\nvalue"}, client: validClient},
		{name: "oversize key", options: prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: strings.Repeat("x", 4097)}, client: validClient},
		{name: "relative URL", options: prowlarr.Options{BaseURL: "/prowlarr", APIKey: secret}, client: validClient},
		{name: "unsupported scheme", options: prowlarr.Options{BaseURL: "ftp://prowlarr.test", APIKey: secret}, client: validClient},
		{name: "credentials in URL", options: prowlarr.Options{BaseURL: "https://user:password@prowlarr.test", APIKey: secret}, client: validClient},
		{name: "query in URL", options: prowlarr.Options{BaseURL: "https://prowlarr.test?key=value", APIKey: secret}, client: validClient},
		{name: "fragment in URL", options: prowlarr.Options{BaseURL: "https://prowlarr.test#fragment", APIKey: secret}, client: validClient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := prowlarr.New(test.options, test.client)
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestSearchSendsAuthenticatedBoundedQueryThroughPathPrefix(t *testing.T) {
	t.Parallel()
	const apiKey = "authorized-prowlarr-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/prowlarr/api/v1/search", request.URL.Path)
		require.Equal(t, "Friends S07E02", request.URL.Query().Get("query"))
		require.Equal(t, "search", request.URL.Query().Get("type"))
		require.Equal(t, "100", request.URL.Query().Get("limit"))
		require.Equal(t, apiKey, request.Header.Get("X-Api-Key"))
		require.Empty(t, request.URL.Query().Get("apikey"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`[]`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL + "/prowlarr/", APIKey: apiKey}, server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewEpisodeSearch("Friends", 1994, 7, 2)
	require.NoError(t, err)

	releases, err := gateway.Search(context.Background(), request)

	require.NoError(t, err)
	require.Empty(t, releases)
	require.Equal(t, "prowlarr", gateway.Name())
	require.Equal(t,
		[]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent, acquisition.ReleaseProtocolUsenet},
		gateway.Capabilities().Protocols(),
	)
}

func TestSearchMapsValidReleasesAndSkipsMalformedRecords(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`[
			{"id":1,"guid":"torrent-guid","size":1000,"indexerId":11,"indexer":"Torrent Indexer","title":"Otherhood.2019.1080p","protocol":"torrent","infoHash":"ABCDEF0123456789ABCDEF0123456789ABCDEF01","magnetUrl":"magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01","downloadUrl":"https://prowlarr.test/download/torrent?id=1","seeders":25},
			{"id":2,"guid":"","size":2000,"indexerId":12,"indexer":"Usenet Indexer","title":"Friends.S07E02.1080p","protocol":"usenet","downloadUrl":"https://prowlarr.test/download/episode.nzb?key=signed"},
			{"id":3,"guid":"unsupported","size":3000,"indexerId":13,"indexer":"Unknown","title":"Unknown","protocol":"unknown","downloadUrl":"https://prowlarr.test/unknown"},
			{"id":4,"guid":"bad-torrent","size":4000,"indexerId":14,"indexer":"Broken","title":"Missing locator","protocol":"torrent"},
			{"id":5,"guid":"bad-url","size":5000,"indexerId":15,"indexer":"Broken","title":"Unsafe URL","protocol":"usenet","downloadUrl":"https://user:password@prowlarr.test/secret"}
		]`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Otherhood", 2019)
	require.NoError(t, err)

	releases, err := gateway.Search(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, releases, 2)
	require.Equal(t, "prowlarr", releases[0].Provider())
	require.Equal(t, "torrent-guid", releases[0].SourceID())
	require.Equal(t, "abcdef0123456789abcdef0123456789abcdef01", releases[0].InfoHash())
	require.Equal(t, 25, releases[0].Seeders())
	require.Equal(t, "12:2", releases[1].SourceID())
	require.Equal(t, acquisition.ReleaseProtocolUsenet, releases[1].Protocol())
	require.Equal(t, "https://prowlarr.test/download/episode.nzb?key=signed", releases[1].DownloadURL())
}

func TestSearchKeepsInfoHashReleaseWhenProwlarrMagnetFieldIsHTTPProxy(t *testing.T) {
	t.Parallel()
	const infoHash = "abcdef0123456789abcdef0123456789abcdef01"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		proxyURL := server.URL + "/api/v1/indexer/1/download?link=magnet"
		_, err := fmt.Fprintf(writer, `[{"guid":"archive-guid","size":209056706,"indexer":"Internet Archive","title":"Big Buck Bunny (2008)","protocol":"torrent","infoHash":%q,"magnetUrl":%q,"downloadUrl":%q,"seeders":1}]`, infoHash, proxyURL, proxyURL)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Big Buck Bunny", 2008)
	require.NoError(t, err)

	releases, err := gateway.Search(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, releases, 1)
	require.Equal(t, infoHash, releases[0].InfoHash())
	require.Empty(t, releases[0].MagnetURL())
	require.Contains(t, releases[0].DownloadURL(), "/api/v1/indexer/1/download")
}

func TestSearchMapsAuthorizationFailure(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
			require.NoError(t, err)
			request, err := acquisition.NewMovieSearch("Movie", 2026)
			require.NoError(t, err)

			_, err = gateway.Search(context.Background(), request)

			require.ErrorIs(t, err, domain.ErrUnauthorized)
		})
	}
}

func TestSearchRejectsBoundaryFailuresWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()
	const apiKey = "prowlarr-secret-key"
	const magnet = "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01"
	const downloadURL = "https://prowlarr.test/download?secret=value"
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "server status", handler: func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(apiKey + " " + magnet + " " + downloadURL))
		}},
		{name: "invalid JSON", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"secret":"` + apiKey + `"`))
		}},
		{name: "oversize body", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", (8<<20)+1)))
		}},
		{name: "redirect", handler: func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, downloadURL, http.StatusFound)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(test.handler)
			t.Cleanup(server.Close)
			gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: apiKey}, server.Client())
			require.NoError(t, err)
			request, err := acquisition.NewMovieSearch("Movie", 2026)
			require.NoError(t, err)

			_, err = gateway.Search(context.Background(), request)

			require.Error(t, err)
			require.NotContains(t, err.Error(), apiKey)
			require.NotContains(t, err.Error(), magnet)
			require.NotContains(t, err.Error(), downloadURL)
		})
	}
}

func TestSearchHonorsCancellationBeforeNetworkRequest(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = gateway.Search(ctx, request)

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, requests.Load())
}

func TestSearchRejectsTransportFailureWithoutRequestURL(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport failed at https://secret.test/download")
	})}
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: "key"}, client)
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)

	_, err = gateway.Search(context.Background(), request)

	require.ErrorContains(t, err, "request Prowlarr search")
	require.NotContains(t, err.Error(), "secret.test")
}

func TestSearchSanitizesResponseCloseFailure(t *testing.T) {
	t.Parallel()
	const secret = "signed-download-secret"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorReadCloser{Reader: strings.NewReader(`[]`), err: errors.New(secret)},
		}, nil
	})}
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: "https://prowlarr.test", APIKey: "key"}, client)
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Movie", 2026)
	require.NoError(t, err)

	_, err = gateway.Search(context.Background(), request)

	require.ErrorContains(t, err, "close Prowlarr search response")
	require.NotContains(t, err.Error(), secret)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type errorReadCloser struct {
	io.Reader
	err error
}

func (closer *errorReadCloser) Close() error { return closer.err }
