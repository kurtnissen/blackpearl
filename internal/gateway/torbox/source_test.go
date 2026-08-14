package torbox

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
		http.Error(writer, "range not implemented", http.StatusNotImplemented)
	}))
	t.Cleanup(server.Close)
	return server
}
