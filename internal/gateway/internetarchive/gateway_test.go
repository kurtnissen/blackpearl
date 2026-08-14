package internetarchive_test

import (
	"context"
	"crypto/sha1" // #nosec G505 -- BitTorrent v1 fixtures use SHA-1 content identities.
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/gateway/internetarchive"
	"github.com/stretchr/testify/require"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("private body detail") }
func (failingReadCloser) Close() error             { return nil }

func TestGatewayMetadataAndConstructionValidation(t *testing.T) {
	t.Parallel()
	gateway, err := internetarchive.New("https://archive.org/", http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "internet-archive", gateway.Name())
	require.True(t, gateway.Capabilities().InfoHashes())
	require.True(t, gateway.Capabilities().MagnetURLs())
	require.True(t, gateway.Capabilities().DownloadURLs())

	for _, test := range []struct {
		name    string
		baseURL string
		client  *http.Client
	}{
		{name: "relative URL", baseURL: "/archive", client: http.DefaultClient},
		{name: "credentials", baseURL: "https://user@example.test/", client: http.DefaultClient},
		{name: "query", baseURL: "https://example.test/?key=value", client: http.DefaultClient},
		{name: "missing client", baseURL: "https://example.test/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := internetarchive.New(test.baseURL, test.client)
			require.Error(t, err)
		})
	}
}

func TestGatewaySearchNormalizesArchiveBitTorrentResults(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/advancedsearch.php", request.URL.Path)
		require.Equal(t, `title:"Tears of Steel" AND year:2012 AND format:"Archive BitTorrent"`, request.URL.Query().Get("q"))
		require.Contains(t, request.URL.Query()["fl[]"], "year")
		require.Equal(t, "100", request.URL.Query().Get("rows"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"response":{"docs":[
			{"identifier":"tears-of-steel_202604","title":"Tears of Steel","year":"2012","item_size":382260309,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"},
			{"identifier":"wrong-year","title":"Tears of Steel","year":2011,"item_size":1,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"},
			{"identifier":"missing-year","title":"Tears of Steel","item_size":1,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"},
			{"identifier":"malformed-year","title":"Tears of Steel","year":{"value":2012},"item_size":1,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"},
			{"identifier":"invalid","title":"Invalid","year":2012,"item_size":0,"btih":"not-a-hash"},
			{"identifier":"../outside","title":"Outside","year":2012,"item_size":1,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"}
		]}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
	require.NoError(t, err)

	releases, err := gateway.Search(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, releases, 1)
	require.Equal(t, "internet-archive", releases[0].Provider())
	require.Equal(t, "tears-of-steel_202604", releases[0].SourceID())
	require.Equal(t, "Tears of Steel (2012)", releases[0].Title())
	require.Equal(t, int64(382260309), releases[0].Size())
	require.Equal(t, "97542f391e9b4c574e721bf95757fe897b0b43fe", releases[0].InfoHash())
	require.Contains(t, releases[0].MagnetURL(), "urn%3Abtih%3A97542f391e9b4c574e721bf95757fe897b0b43fe")
	require.Equal(t, server.URL+"/download/tears-of-steel_202604/tears-of-steel_202604_archive.torrent", releases[0].DownloadURL())
}

func TestGatewaySearchBuildsEpisodeIntentAndBoundsProviderResponses(t *testing.T) {
	t.Parallel()
	t.Run("episode", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			require.Equal(t, `title:"Example Show" AND title:"S07E02" AND format:"Archive BitTorrent"`, request.URL.Query().Get("q"))
			_, err := writer.Write([]byte(`{"response":{"docs":[]}}`))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		gateway, err := internetarchive.New(server.URL+"/", server.Client())
		require.NoError(t, err)
		request, err := acquisition.NewEpisodeSearch("Example Show", 1994, 7, 2)
		require.NoError(t, err)

		releases, err := gateway.Search(context.Background(), request)

		require.NoError(t, err)
		require.Empty(t, releases)
	})

	for _, test := range []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "provider status", statusCode: http.StatusServiceUnavailable, body: "unavailable"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: "not-json"},
		{name: "oversized body", statusCode: http.StatusOK, body: strings.Repeat("x", (2<<20)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.statusCode)
				_, err := writer.Write([]byte(test.body))
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			gateway, err := internetarchive.New(server.URL+"/", server.Client())
			require.NoError(t, err)
			request, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
			require.NoError(t, err)

			_, err = gateway.Search(context.Background(), request)

			require.Error(t, err)
		})
	}
}

func TestGatewaySearchPreservesCallerCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("canceled search must not reach the provider")
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = gateway.Search(ctx, request)

	require.ErrorIs(t, err, context.Canceled)
}

func TestGatewaySanitizesSearchAndMaterializationTransportFailures(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private transport detail")
	})}
	gateway, err := internetarchive.New("https://archive.org/", client)
	require.NoError(t, err)
	search, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
	require.NoError(t, err)

	_, err = gateway.Search(context.Background(), search)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "private transport detail")

	hash := "97542f391e9b4c574e721bf95757fe897b0b43fe"
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: "https://archive.org/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	_, err = gateway.Materialize(context.Background(), release)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "private transport detail")
}

func TestGatewaySanitizesSearchAndMaterializationReadFailures(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       failingReadCloser{},
		}, nil
	})}
	gateway, err := internetarchive.New("https://archive.org/", client)
	require.NoError(t, err)
	search, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
	require.NoError(t, err)

	_, err = gateway.Search(context.Background(), search)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "private body detail")

	hash := "97542f391e9b4c574e721bf95757fe897b0b43fe"
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: "https://archive.org/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	_, err = gateway.Materialize(context.Background(), release)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "private body detail")
}

func TestGatewayMaterializesVerifiedArchiveTorrentFileBeforeMagnet(t *testing.T) {
	t.Parallel()
	payload := []byte("d4:infod6:lengthi1e4:name4:testee")
	info := []byte("d6:lengthi1e4:name4:teste")
	digest := sha1.Sum(info) // #nosec G401 -- BitTorrent v1 defines its content identity with SHA-1.
	hash := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/download/fixture/fixture_archive.torrent", request.URL.Path)
		writer.Header().Set("Content-Type", "application/x-bittorrent")
		_, writeErr := writer.Write(payload)
		require.NoError(t, writeErr)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: server.URL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	material, err := gateway.Materialize(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputFile, material.Kind())
	require.Equal(t, hash, material.InfoHash())
	require.Equal(t, payload, material.File())
}

func TestGatewayMaterializationRejectsUntrustedOrInvalidTorrentResponses(t *testing.T) {
	t.Parallel()
	validPayload := []byte("d4:infod6:lengthi1e4:name4:testee")
	info := []byte("d6:lengthi1e4:name4:teste")
	digest := sha1.Sum(info) // #nosec G401 -- BitTorrent v1 defines its content identity with SHA-1.
	hash := hex.EncodeToString(digest[:])

	tests := []struct {
		name        string
		contentType string
		payload     []byte
		offOrigin   bool
	}{
		{name: "HTML response", contentType: "text/html", payload: []byte("not a torrent")},
		{name: "mismatched info hash", contentType: "application/x-bittorrent", payload: []byte("d4:infod6:lengthi2e4:name4:testee")},
		{name: "oversized response", contentType: "application/x-bittorrent", payload: make([]byte, acquisition.MaximumTorrentFileBytes+1)},
		{name: "off-origin material URL", contentType: "application/x-bittorrent", payload: validPayload, offOrigin: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, writeErr := writer.Write(test.payload)
				if writeErr != nil && writeErr != io.ErrClosedPipe {
					require.NoError(t, writeErr)
				}
			}))
			t.Cleanup(server.Close)
			gateway, err := internetarchive.New(server.URL+"/", server.Client())
			require.NoError(t, err)
			downloadURL := server.URL + "/download/fixture/fixture_archive.torrent"
			if test.offOrigin {
				downloadURL = "https://outside.invalid/fixture.torrent"
			}
			release, releaseErr := acquisition.NewRelease(acquisition.ReleaseInput{
				Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
				Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
				InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash, DownloadURL: downloadURL,
			})
			require.NoError(t, releaseErr)

			_, err = gateway.Materialize(context.Background(), release)

			require.Error(t, err)
		})
	}
}

func TestGatewayMaterializationAllowsSameOriginRedirectAndRejectsUntrustedRedirect(t *testing.T) {
	t.Parallel()
	payload := []byte("d4:infod6:lengthi1e4:name4:testee")
	info := []byte("d6:lengthi1e4:name4:teste")
	digest := sha1.Sum(info) // #nosec G401 -- BitTorrent v1 defines its content identity with SHA-1.
	hash := hex.EncodeToString(digest[:])

	var sameOrigin *httptest.Server
	sameOrigin = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/download/fixture/fixture_archive.torrent" {
			http.Redirect(writer, request, sameOrigin.URL+"/cdn/fixture.torrent", http.StatusFound)
			return
		}
		require.Equal(t, "/cdn/fixture.torrent", request.URL.Path)
		writer.Header().Set("Content-Type", "application/x-bittorrent")
		_, err := writer.Write(payload)
		require.NoError(t, err)
	}))
	t.Cleanup(sameOrigin.Close)
	gateway, err := internetarchive.New(sameOrigin.URL+"/", sameOrigin.Client())
	require.NoError(t, err)
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: sameOrigin.URL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	material, err := gateway.Materialize(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, payload, material.File())

	outside := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, writeErr := writer.Write(payload)
		require.NoError(t, writeErr)
	}))
	t.Cleanup(outside.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, outside.URL+"/fixture.torrent", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	untrustedGateway, err := internetarchive.New(redirector.URL+"/", redirector.Client())
	require.NoError(t, err)
	untrustedRelease, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: redirector.URL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	_, err = untrustedGateway.Materialize(context.Background(), untrustedRelease)

	require.Error(t, err)
}

func TestGatewayMaterializationValidatesContextReleaseAndProviderStatus(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	hash := "97542f391e9b4c574e721bf95757fe897b0b43fe"
	valid, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: server.URL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)
	wrongProvider, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "other", SourceID: "fixture", Title: "Fixture (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 1, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: server.URL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)

	_, err = gateway.Materialize(context.Background(), wrongProvider)
	require.Error(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = gateway.Materialize(ctx, valid)
	require.ErrorIs(t, err, context.Canceled)

	_, err = gateway.Materialize(context.Background(), valid)
	require.Error(t, err)
}
