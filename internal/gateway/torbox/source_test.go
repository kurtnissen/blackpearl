package torbox

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

type cdnMutation func(*cdnResponse)

type cdnResponse struct {
	status       int
	contentRange string
	body         []byte
}

func newTestCDN(t *testing.T, content []byte, mutate cdnMutation) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Empty(t, request.Header.Get("Authorization"))
		require.Empty(t, request.URL.Query().Get("token"))
		if request.Method == http.MethodHead {
			writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
			writer.Header().Set("Accept-Ranges", "bytes")
			writer.WriteHeader(http.StatusOK)
			return
		}
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var start int
		var end int
		_, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if err != nil || start < 0 || end < start || end >= len(content) {
			http.Error(writer, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response := cdnResponse{
			status:       http.StatusPartialContent,
			contentRange: fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)),
			body:         append([]byte(nil), content[start:end+1]...),
		}
		if mutate != nil {
			mutate(&response)
		}
		writer.Header().Set("Content-Range", response.contentRange)
		writer.Header().Set("Content-Length", fmt.Sprint(len(response.body)))
		writer.WriteHeader(response.status)
		_, err = writer.Write(response.body)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSourceReadAtReturnsExactRangesAndEOF(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	cdn := newTestCDN(t, content, nil)
	source := openTestTorBoxSource(t, cdn, 16)

	interior := make([]byte, 4)
	count, err := source.ReadAt(context.Background(), interior, 4)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, "4567", string(interior))

	final := make([]byte, 4)
	count, err = source.ReadAt(context.Background(), final, 14)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 2, count)
	require.Equal(t, "ef", string(final[:count]))

	count, err = source.ReadAt(context.Background(), make([]byte, 1), 16)
	require.ErrorIs(t, err, io.EOF)
	require.Zero(t, count)
}

func TestSourceReadAtRejectsInvalidCDNResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  cdnMutation
		message string
	}{
		{name: "ignored range", mutate: func(response *cdnResponse) { response.status = http.StatusOK }, message: "status 206"},
		{name: "wrong content range", mutate: func(response *cdnResponse) { response.contentRange = "bytes 0-3/16" }, message: "Content-Range"},
		{name: "short body", mutate: func(response *cdnResponse) { response.body = response.body[:2] }, message: "body length"},
		{name: "oversized body", mutate: func(response *cdnResponse) { response.body = append(response.body, 'x') }, message: "body length"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cdn := newTestCDN(t, []byte("0123456789abcdef"), test.mutate)
			source := openTestTorBoxSource(t, cdn, 16)

			_, err := source.ReadAt(context.Background(), make([]byte, 4), 4)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestSourceReadAtRefreshesExpiredLinkOnce(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	var expiredCalls atomic.Int64
	expired := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if expiredCalls.Add(1) == 1 {
			require.Equal(t, "bytes=0-0", request.Header.Get("Range"))
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(content)))
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write(content[:1])
			require.NoError(t, err)
			return
		}
		http.Error(writer, "expired", http.StatusForbidden)
	}))
	t.Cleanup(expired.Close)
	fresh := newTestCDN(t, content, nil)
	var linkCalls atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			writeTorrentMetadata(writer, 17, 3, 16)
		case "/v1/api/torrents/requestdl":
			call := linkCalls.Add(1)
			value := expired.URL
			if call > 1 {
				value = fresh.URL
			}
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", value))
		default:
			http.NotFound(writer, request)
		}
	})
	client := fresh.Client()
	client.Transport = expired.Client().Transport
	gateway := newTestGateway(t, api.URL+"/v1/api/", client)
	opened, err := gateway.Open(context.Background(), domainBacking("17:3"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	buffer := make([]byte, 4)
	count, err := opened.ReadAt(context.Background(), buffer, 4)

	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.Equal(t, "4567", string(buffer))
	require.Equal(t, int64(2), expiredCalls.Load())
	require.Equal(t, int64(2), linkCalls.Load())
}

func TestSourceReadAtRejectsRefreshedLinkWithWrongSize(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	var expiredCalls atomic.Int64
	expired := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if expiredCalls.Add(1) == 1 {
			require.Equal(t, "bytes=0-0", request.Header.Get("Range"))
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write(content[:1])
			require.NoError(t, err)
			return
		}
		http.Error(writer, "expired", http.StatusGone)
	}))
	t.Cleanup(expired.Close)
	wrong := newTestCDN(t, []byte("short"), nil)
	var linkCalls atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			writeTorrentMetadata(writer, 17, 3, int64(len(content)))
		case "/v1/api/torrents/requestdl":
			value := expired.URL
			if linkCalls.Add(1) > 1 {
				value = wrong.URL
			}
			writeEnvelope(writer, true, "ok", fmt.Sprintf("%q", value))
		default:
			http.NotFound(writer, request)
		}
	})
	client := wrong.Client()
	client.Transport = expired.Client().Transport
	gateway := newTestGateway(t, api.URL+"/v1/api/", client)
	opened, err := gateway.Open(context.Background(), domainBacking("17:3"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	_, err = opened.ReadAt(context.Background(), make([]byte, 4), 0)

	require.ErrorContains(t, err, "size mismatch")
}

func TestSourceReadAtHonorsContextAndClose(t *testing.T) {
	t.Parallel()
	cdn := newTestCDN(t, []byte("0123456789abcdef"), nil)
	source := openTestTorBoxSource(t, cdn, 16)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := source.ReadAt(cancelled, make([]byte, 4), 0)
	require.ErrorIs(t, err, context.Canceled)
	_, err = source.ReadAt(context.Background(), make([]byte, 1), -1)
	require.ErrorContains(t, err, "negative")
	require.NoError(t, source.Close())
	_, err = source.ReadAt(context.Background(), make([]byte, 4), 0)
	require.ErrorContains(t, err, "closed")
}

func openTestTorBoxSource(t *testing.T, cdn *httptest.Server, size int64) *source {
	t.Helper()
	gateway := newTestGateway(t, "http://example.invalid/v1/api/", cdn.Client())
	identifier, err := parseObjectID("17:3")
	require.NoError(t, err)
	result := newSource(gateway, identifier, fileMetadata{size: size, validator: "torbox:hash:test:16"}, mustParseURL(t, cdn.URL))
	t.Cleanup(func() {
		if !result.closed.Load() {
			require.NoError(t, result.Close())
		}
	})
	return result
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	return parsed
}

func domainBacking(object string) domain.BackingRef {
	return domain.BackingRef{Provider: providerName, ObjectID: object}
}
