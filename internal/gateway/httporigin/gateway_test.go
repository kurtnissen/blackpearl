package httporigin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{"", "range-origin/media", "ftp://range-origin/media"} {
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()

			_, err := New(rawURL, http.DefaultClient)

			require.Error(t, err)
		})
	}
}

func TestOpenRejectsUnsupportedBacking(t *testing.T) {
	t.Parallel()
	origin := newRangeOrigin(t, []byte("0123456789abcdef"), nil)
	gateway, err := New(origin.URL+"/media/", origin.Client())
	require.NoError(t, err)
	tests := []domain.BackingRef{
		{Provider: "other", ObjectID: "movie.mp4"},
		{Provider: "http-range", ObjectID: "../movie.mp4"},
		{Provider: "http-range", ObjectID: "folder/movie.mp4"},
	}
	for _, backing := range tests {
		_, err := gateway.Open(context.Background(), backing)

		require.Error(t, err)
	}
}

func TestOpenRejectsRedirects(t *testing.T) {
	t.Parallel()
	destination := newRangeOrigin(t, []byte("0123456789abcdef"), nil)
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL+"/media/movie.mp4", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	gateway, err := New(redirect.URL+"/media/", redirect.Client())
	require.NoError(t, err)

	_, err = gateway.Open(context.Background(), domain.BackingRef{Provider: "http-range", ObjectID: "movie.mp4"})

	require.ErrorContains(t, err, "status 200")
}

func TestOpenUsesLastModifiedWhenETagIsWeak(t *testing.T) {
	t.Parallel()
	var observedIfRange string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `W/"weak"`)
		writer.Header().Set("Last-Modified", "Thu, 13 Aug 2026 12:00:00 GMT")
		if request.Method == http.MethodHead {
			writer.Header().Set("Content-Length", "4")
			writer.WriteHeader(http.StatusOK)
			return
		}
		observedIfRange = request.Header.Get("If-Range")
		writer.Header().Set("Content-Range", "bytes 0-3/4")
		writer.Header().Set("Last-Modified", "Thu, 13 Aug 2026 12:00:00 GMT")
		writer.WriteHeader(http.StatusPartialContent)
		_, err := writer.Write([]byte("data"))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := New(server.URL+"/media/", server.Client())
	require.NoError(t, err)
	opened, err := gateway.Open(context.Background(), domain.BackingRef{Provider: "http-range", ObjectID: "movie.mp4"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	_, err = opened.ReadAt(context.Background(), make([]byte, 4), 0)

	require.NoError(t, err)
	require.Equal(t, "Thu, 13 Aug 2026 12:00:00 GMT", observedIfRange)
	require.Equal(t, "last-modified:Thu, 13 Aug 2026 12:00:00 GMT", opened.Validator())
}

func TestOpenRejectsObjectWithoutImmutableValidator(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", "4")
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	gateway, err := New(server.URL+"/media/", server.Client())
	require.NoError(t, err)

	_, err = gateway.Open(context.Background(), domain.BackingRef{Provider: "http-range", ObjectID: "movie.mp4"})

	require.ErrorContains(t, err, "validator")
}

func TestSourceReadAtReturnsExactRangesAndEOF(t *testing.T) {
	t.Parallel()
	origin := newRangeOrigin(t, []byte("0123456789abcdef"), nil)
	source := openTestSource(t, origin)

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

func TestSourceReadAtRejectsInvalidOriginResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*rangeResponse)
		message string
	}{
		{
			name: "ignored range",
			mutate: func(response *rangeResponse) {
				response.status = http.StatusOK
			},
			message: "status 206",
		},
		{
			name: "wrong content range",
			mutate: func(response *rangeResponse) {
				response.contentRange = "bytes 0-3/16"
			},
			message: "Content-Range",
		},
		{
			name: "short body",
			mutate: func(response *rangeResponse) {
				response.body = response.body[:2]
			},
			message: "body length",
		},
		{
			name: "oversized body",
			mutate: func(response *rangeResponse) {
				response.body = append(response.body, 'x')
			},
			message: "body length",
		},
		{
			name: "changed validator",
			mutate: func(response *rangeResponse) {
				response.etag = `"fixture-v2"`
			},
			message: "validator",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			origin := newRangeOrigin(t, []byte("0123456789abcdef"), test.mutate)
			source := openTestSource(t, origin)

			_, err := source.ReadAt(context.Background(), make([]byte, 4), 4)

			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestSourceReadAtHonorsContextAndClose(t *testing.T) {
	t.Parallel()
	origin := newRangeOrigin(t, []byte("0123456789abcdef"), nil)
	source := openTestSource(t, origin)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := source.ReadAt(cancelled, make([]byte, 4), 0)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, source.Close())
	_, err = source.ReadAt(context.Background(), make([]byte, 4), 0)
	require.ErrorContains(t, err, "closed")
}

type rangeResponse struct {
	status       int
	contentRange string
	body         []byte
	etag         string
}

func newRangeOrigin(t *testing.T, content []byte, mutate func(*rangeResponse)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/media/movie.mp4" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("ETag", `"fixture-v1"`)
		writer.Header().Set("Accept-Ranges", "bytes")
		switch request.Method {
		case http.MethodHead:
			writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
			writer.WriteHeader(http.StatusOK)
		case http.MethodGet:
			var start int
			var end int
			_, scanErr := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			if scanErr != nil || start < 0 || end < start || end >= len(content) {
				http.Error(writer, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			response := rangeResponse{
				status:       http.StatusPartialContent,
				contentRange: fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)),
				body:         append([]byte(nil), content[start:end+1]...),
				etag:         `"fixture-v1"`,
			}
			if mutate != nil {
				mutate(&response)
			}
			writer.Header().Set("Content-Range", response.contentRange)
			writer.Header().Set("ETag", response.etag)
			writer.Header().Set("Content-Length", fmt.Sprint(len(response.body)))
			writer.WriteHeader(response.status)
			_, writeErr := writer.Write(response.body)
			require.NoError(t, writeErr)
		default:
			writer.Header().Set("Allow", strings.Join([]string{http.MethodHead, http.MethodGet}, ", "))
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func openTestSource(t *testing.T, origin *httptest.Server) *Source {
	t.Helper()
	gateway, err := New(origin.URL+"/media/", origin.Client())
	require.NoError(t, err)
	backing, err := domain.NewBackingRef("http-range", "movie.mp4")
	require.NoError(t, err)
	opened, err := gateway.Open(context.Background(), backing)
	require.NoError(t, err)
	source, ok := opened.(*Source)
	require.True(t, ok)
	t.Cleanup(func() {
		if !source.closed.Load() {
			require.NoError(t, source.Close())
		}
	})
	return source
}
