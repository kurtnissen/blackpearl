package webui_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	webui "github.com/blackpearl-media/blackpearl/web"
	"github.com/stretchr/testify/require"
)

func TestHandlerServesEmbeddedSetupApplication(t *testing.T) {
	t.Parallel()
	handler, err := webui.Handler()
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "BlackPearl")
	require.Contains(t, response.Header().Get("Content-Type"), "text/html")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Header().Get("Content-Security-Policy"), "script-src 'self' 'unsafe-inline'")
	assetPattern := regexp.MustCompile(`src="([^"]+\.js)"`)
	match := assetPattern.FindStringSubmatch(response.Body.String())
	require.Len(t, match, 2)
	assetRequest := httptest.NewRequest(http.MethodGet, match[1], nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	require.Equal(t, http.StatusOK, assetResponse.Code)
	require.Contains(t, assetResponse.Header().Get("Content-Type"), "javascript")
}

func TestHandlerRejectsTraversalAndUnsupportedMethods(t *testing.T) {
	t.Parallel()
	handler, err := webui.Handler()
	require.NoError(t, err)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/", nil),
		httptest.NewRequest(http.MethodGet, "/../package.json", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.NotEqual(t, http.StatusOK, response.Code)
	}
}
