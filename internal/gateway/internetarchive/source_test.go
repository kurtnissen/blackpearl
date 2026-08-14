package internetarchive_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/gateway/internetarchive"
	"github.com/stretchr/testify/require"
)

func TestGatewayOpensMetadataOnlyAndReadsExactHTTP11Ranges(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	getRequests := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/metadata/fixture":
			_, err := fmt.Fprintf(writer, `{
				"metadata":{"licenseurl":"https://creativecommons.org/licenses/by/4.0/"},
				"files":[{"name":"movie.mp4","size":%d,"sha1":"1111111111111111111111111111111111111111","source":"original"}]
			}`, len(content))
			require.NoError(t, err)
		case "/download/fixture/movie.mp4":
			require.Equal(t, 1, request.ProtoMajor)
			writer.Header().Set("ETag", `"fixture-etag"`)
			writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
			if request.Method == http.MethodHead {
				return
			}
			getRequests++
			require.Equal(t, "bytes=4-7", request.Header.Get("Range"))
			require.Equal(t, `"fixture-etag"`, request.Header.Get("If-Range"))
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes 4-7/%d", len(content)))
			writer.Header().Set("Content-Length", "4")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write(content[4:8])
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	candidates, err := gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))
	require.NoError(t, err)

	source, err := gateway.Open(context.Background(), candidates[0].Media().Backing())
	require.NoError(t, err)
	require.Zero(t, getRequests, "opening metadata must not read content bytes")
	require.Equal(t, int64(len(content)), source.Size())
	require.Equal(t, "internet-archive:sha1:1111111111111111111111111111111111111111", source.Validator())
	buffer := make([]byte, 4)
	n, err := source.ReadAt(context.Background(), buffer, 4)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, content[4:8], buffer)
	require.Equal(t, 1, getRequests)
	require.NoError(t, source.Close())
	_, err = source.ReadAt(context.Background(), buffer, 0)
	require.Error(t, err)
}

func TestArchiveRangeSourceReturnsEOFAtLogicalEnd(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789abcdef")
	server := archiveRangeServer(t, content, nil)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	candidates, err := gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))
	require.NoError(t, err)
	source, err := gateway.Open(context.Background(), candidates[0].Media().Backing())
	require.NoError(t, err)

	buffer := make([]byte, 8)
	n, err := source.ReadAt(context.Background(), buffer, 14)

	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 2, n)
	require.Equal(t, content[14:], buffer[:n])
}

func TestArchiveRangeSourceRejectsWrongContentRange(t *testing.T) {
	t.Parallel()
	server := archiveRangeServer(t, []byte("0123456789abcdef"), func(header http.Header) {
		header.Set("Content-Range", "bytes 0-3/16")
	})
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	candidates, err := gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))
	require.NoError(t, err)
	source, err := gateway.Open(context.Background(), candidates[0].Media().Backing())
	require.NoError(t, err)

	_, err = source.ReadAt(context.Background(), make([]byte, 4), 4)

	require.ErrorContains(t, err, "Content-Range")
}

func TestGatewayOpenRejectsChangedSizeAndMissingHTTPValidator(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		headSize  int
		withETag  bool
		wantError string
	}{
		{name: "changed size", headSize: 15, withETag: true, wantError: "size changed"},
		{name: "missing validator", headSize: 16, withETag: false, wantError: "validator"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/metadata/fixture":
					_, err := writer.Write([]byte(`{
						"metadata":{"licenseurl":"https://creativecommons.org/licenses/by/4.0/"},
						"files":[{"name":"movie.mp4","size":16,"sha1":"1111111111111111111111111111111111111111","source":"original"}]
					}`))
					require.NoError(t, err)
				case "/download/fixture/movie.mp4":
					writer.Header().Set("Content-Length", fmt.Sprint(test.headSize))
					if test.withETag {
						writer.Header().Set("ETag", `"fixture-etag"`)
					}
				default:
					http.NotFound(writer, request)
				}
			}))
			t.Cleanup(server.Close)
			gateway, err := internetarchive.New(server.URL+"/", server.Client())
			require.NoError(t, err)
			candidates, err := gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))
			require.NoError(t, err)

			_, err = gateway.Open(context.Background(), candidates[0].Media().Backing())

			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestGatewayFileOperationsHonorCancellationBeforeIO(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("canceled file operation must not reach provider")
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = gateway.ListRangeCandidates(canceled, archiveFixtureRelease(t, server.URL))
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, gateway.Ready(canceled), context.Canceled)
}

func archiveRangeServer(t *testing.T, content []byte, mutate func(http.Header)) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/metadata/fixture":
			_, err := fmt.Fprintf(writer, `{
				"metadata":{"licenseurl":"https://creativecommons.org/licenses/by/4.0/"},
				"files":[{"name":"movie.mp4","size":%d,"sha1":"1111111111111111111111111111111111111111","source":"original"}]
			}`, len(content))
			require.NoError(t, err)
		case "/download/fixture/movie.mp4":
			writer.Header().Set("ETag", `"fixture-etag"`)
			writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
			if request.Method == http.MethodHead {
				return
			}
			var start, end int64
			_, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			require.NoError(t, err)
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			writer.Header().Set("Content-Length", fmt.Sprint(end-start+1))
			if mutate != nil {
				mutate(writer.Header())
			}
			writer.WriteHeader(http.StatusPartialContent)
			_, err = writer.Write(content[start : end+1])
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
