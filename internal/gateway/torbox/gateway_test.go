package torbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayCoalescesConcurrentDownloadLinkRequests(t *testing.T) {
	t.Parallel()
	cdn := newTestCDN(t, []byte("0123456789abcdef"), nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var linkCalls atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			writeTorrentMetadata(writer, 17, 3, 16)
		case "/v1/api/torrents/requestdl":
			linkCalls.Add(1)
			startOnce.Do(func() { close(started) })
			<-release
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", cdn.URL))
		default:
			http.NotFound(writer, request)
		}
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", cdn.Client())
	backing := domain.BackingRef{Provider: providerName, ObjectID: "17:3"}
	// Prime metadata so this test isolates download-link coalescing.
	_, err := gateway.loadMetadata(context.Background(), objectID{TorrentID: 17, FileID: 3})
	require.NoError(t, err)

	results := make(chan error, 16)
	for range 16 {
		go func() {
			opened, openErr := gateway.Open(context.Background(), backing)
			if openErr == nil {
				openErr = opened.Close()
			}
			results <- openErr
		}()
	}
	<-started
	close(release)
	for range 16 {
		require.NoError(t, <-results)
	}
	require.Equal(t, int64(1), linkCalls.Load())
}

func TestGatewayOpenMapsCompletedTorrentFile(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	cdn := newTestCDN(t, content, nil)
	var metadataCalls atomic.Int64
	var linkCalls atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			metadataCalls.Add(1)
			require.Equal(t, "17", request.URL.Query().Get("id"))
			require.Equal(t, "true", request.URL.Query().Get("bypass_cache"))
			writeTorrentMetadata(writer, 17, 3, int64(len(content)))
		case "/v1/api/torrents/requestdl":
			linkCalls.Add(1)
			require.Equal(t, "test-token", request.URL.Query().Get("token"))
			require.Equal(t, "17", request.URL.Query().Get("torrent_id"))
			require.Equal(t, "3", request.URL.Query().Get("file_id"))
			require.Equal(t, "false", request.URL.Query().Get("redirect"))
			require.Equal(t, "false", request.URL.Query().Get("append_name"))
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", cdn.URL))
		default:
			http.NotFound(writer, request)
		}
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", cdn.Client())
	backing, err := domain.NewBackingRef(providerName, "17:3")
	require.NoError(t, err)

	opened, err := gateway.Open(context.Background(), backing)

	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	require.Equal(t, int64(len(content)), opened.Size())
	require.Equal(t, "torbox:hash:sha256-file:16", opened.Validator())
	require.Equal(t, int64(1), metadataCalls.Load())
	require.Equal(t, int64(1), linkCalls.Load())
}

func TestGatewayOpenRejectsCDNSizeMismatchWithoutLeakingURL(t *testing.T) {
	t.Parallel()
	cdn := newTestCDN(t, []byte("short"), nil)
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			writeTorrentMetadata(writer, 17, 3, 16)
		case "/v1/api/torrents/requestdl":
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", cdn.URL))
		default:
			http.NotFound(writer, request)
		}
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", cdn.Client())

	_, err := gateway.Open(context.Background(), domainBacking("17:3"))

	require.ErrorContains(t, err, "size mismatch")
	require.NotContains(t, err.Error(), cdn.URL)
}

func TestGatewayErrorsNeverExposeConfiguredToken(t *testing.T) {
	t.Parallel()
	secret := "real-secret-value"
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(writer, `{"success":false,"detail":%q,"data":null}`, "denied "+secret)
		require.NoError(t, err)
	})
	gateway, err := New(Options{
		APIBaseURL:  api.URL + "/v1/api/",
		APIToken:    secret,
		MetadataTTL: time.Minute,
		LinkTTL:     2 * time.Hour,
	}, api.Client())
	require.NoError(t, err)

	_, err = gateway.Open(context.Background(), domainBacking("17:3"))

	require.ErrorContains(t, err, "denied")
	require.NotContains(t, err.Error(), secret)
}

func TestGatewayDownloadTransportErrorNeverExposesToken(t *testing.T) {
	t.Parallel()
	secret := "transport-secret-value"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("failed request to %s", request.URL.String())
	})}
	gateway, err := New(Options{
		APIBaseURL:  "https://api.example.invalid/v1/api/",
		APIToken:    secret,
		MetadataTTL: time.Minute,
		LinkTTL:     2 * time.Hour,
	}, client)
	require.NoError(t, err)

	_, err = gateway.requestDownloadURL(context.Background(), objectID{TorrentID: 17, FileID: 3})

	require.ErrorContains(t, err, "request TorBox download link")
	require.NotContains(t, err.Error(), secret)
}

func TestGatewayErrorsNeverExposeDownloadURLs(t *testing.T) {
	t.Parallel()
	signedURL := "https://signed.example.invalid/file?signature=sensitive"
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		writeEnvelope(writer, false, "denied ("+signedURL+")", "null")
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	_, err := gateway.Open(context.Background(), domainBacking("17:3"))

	require.ErrorContains(t, err, "denied")
	require.NotContains(t, err.Error(), signedURL)
	require.NotContains(t, err.Error(), "signature=sensitive")
}

func TestNewRejectsAPIBaseCredentialsQueryAndFragment(t *testing.T) {
	t.Parallel()
	tests := []string{
		"https://user:secret@api.example.invalid/v1/api/",
		"https://api.example.invalid/v1/api/?token=secret",
		"https://api.example.invalid/v1/api/#secret",
	}
	for _, baseURL := range tests {
		t.Run(baseURL, func(t *testing.T) {
			t.Parallel()

			_, err := New(Options{
				APIBaseURL:  baseURL,
				APIToken:    "test-token",
				MetadataTTL: time.Minute,
				LinkTTL:     2 * time.Hour,
			}, http.DefaultClient)

			require.ErrorContains(t, err, "API base")
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestGatewayOpenCachesMetadataAndLinkWithinTTL(t *testing.T) {
	t.Parallel()
	cdn := newTestCDN(t, []byte("0123456789abcdef"), nil)
	var metadataCalls atomic.Int64
	var linkCalls atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			metadataCalls.Add(1)
			writeTorrentMetadata(writer, 17, 3, 16)
		case "/v1/api/torrents/requestdl":
			linkCalls.Add(1)
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", cdn.URL))
		default:
			http.NotFound(writer, request)
		}
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", cdn.Client())
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	gateway.now = func() time.Time { return now }
	backing := domain.BackingRef{Provider: providerName, ObjectID: "17:3"}

	first, err := gateway.Open(context.Background(), backing)
	require.NoError(t, err)
	require.NoError(t, first.Close())
	second, err := gateway.Open(context.Background(), backing)
	require.NoError(t, err)
	require.NoError(t, second.Close())
	require.Equal(t, int64(1), metadataCalls.Load())
	require.Equal(t, int64(1), linkCalls.Load())

	now = now.Add(61 * time.Second)
	third, err := gateway.Open(context.Background(), backing)
	require.NoError(t, err)
	require.NoError(t, third.Close())
	require.Equal(t, int64(2), metadataCalls.Load())
	require.Equal(t, int64(1), linkCalls.Load())
}

func TestGatewayOpenRejectsUnsafeOrUnavailableFilesWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "failed envelope", data: `{"success":false,"detail":"denied test-token","data":null}`, want: "denied"},
		{name: "unfinished", data: torrentJSON(17, 3, 16, `"download_finished":false,"download_present":true`, `"hash":"sha256-file"`), want: "not complete"},
		{name: "missing download", data: torrentJSON(17, 3, 16, `"download_finished":true,"download_present":false`, `"hash":"sha256-file"`), want: "not present"},
		{name: "zipped", data: torrentJSON(17, 3, 16, `"download_finished":true,"download_present":true`, `"hash":"sha256-file","zipped":true`), want: "zipped"},
		{name: "infected", data: torrentJSON(17, 3, 16, `"download_finished":true,"download_present":true`, `"hash":"sha256-file","infected":true`), want: "infected"},
		{name: "zero size", data: torrentJSON(17, 3, 0, `"download_finished":true,"download_present":true`, `"hash":"sha256-file"`), want: "positive size"},
		{name: "hashless", data: torrentJSON(17, 3, 16, `"download_finished":true,"download_present":true`, ``), want: "stable hash"},
		{name: "wrong torrent", data: torrentJSON(18, 3, 16, `"download_finished":true,"download_present":true`, `"hash":"sha256-file"`), want: "torrent 17"},
		{name: "wrong file", data: torrentJSON(17, 4, 16, `"download_finished":true,"download_present":true`, `"hash":"sha256-file"`), want: "file 3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(test.data))
				require.NoError(t, err)
			})
			gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

			_, err := gateway.Open(context.Background(), domain.BackingRef{Provider: providerName, ObjectID: "17:3"})

			require.ErrorContains(t, err, test.want)
			require.NotContains(t, err.Error(), "test-token")
		})
	}
}

func newTestGateway(t *testing.T, baseURL string, client *http.Client) *Gateway {
	t.Helper()
	gateway, err := New(Options{
		APIBaseURL:  baseURL,
		APIToken:    "test-token",
		MetadataTTL: time.Minute,
		LinkTTL:     2 * time.Hour,
	}, client)
	require.NoError(t, err)
	return gateway
}

func newTestAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeTorrentMetadata(writer http.ResponseWriter, torrentID int64, fileID int64, size int64) {
	writeEnvelope(writer, true, "ok", fmt.Sprintf(`{"id":%d,"download_finished":true,"download_present":true,"files":[{"id":%d,"name":"movie.mp4","size":%d,"hash":"sha256-file","md5":"md5-file","zipped":false,"infected":false}]}`, torrentID, fileID, size))
}

func torrentJSON(torrentID int64, fileID int64, size int64, torrentFields string, fileFields string) string {
	if fileFields != "" {
		fileFields = "," + fileFields
	}
	return fmt.Sprintf(`{"success":true,"detail":"ok","data":{"id":%d,%s,"files":[{"id":%d,"name":"movie.mp4","size":%d%s}]}}`, torrentID, torrentFields, fileID, size, fileFields)
}

func writeEnvelope(writer http.ResponseWriter, success bool, detail string, rawData string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"success":%t,"detail":%q,"data":%s}`, success, detail, rawData)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
