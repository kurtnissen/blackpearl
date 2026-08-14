package httpserver_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/httpserver"
	"github.com/stretchr/testify/require"
)

func TestHealthIsLiveWithoutConsultingReadiness(t *testing.T) {
	t.Parallel()
	readiness := &fakeReadiness{err: errors.New("not ready")}
	handler := httpserver.New(readiness, testLogger())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"status":"ok"}`, response.Body.String())
	require.False(t, readiness.called)
	require.NotEmpty(t, response.Header().Get("X-Request-Id"))
}

func TestReadinessReflectsDependencyState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "ready", wantStatus: http.StatusOK, wantBody: `{"status":"ready"}`},
		{name: "not ready", err: errors.New("catalog empty"), wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"not_ready"}`},
		{name: "setup required", err: domain.ErrNotConfigured, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"setup_required"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			readiness := &fakeReadiness{err: test.err}
			handler := httpserver.New(readiness, testLogger())
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, test.wantStatus, response.Code)
			require.JSONEq(t, test.wantBody, response.Body.String())
			require.True(t, readiness.called)
		})
	}
}

func TestServerRoutesSetupAPIAndEmbeddedUI(t *testing.T) {
	t.Parallel()
	api := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
	})
	ui := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})
	handler := httpserver.New(&fakeReadiness{}, testLogger(), httpserver.Options{SetupAPI: api, UI: ui})

	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	uiResponse := httptest.NewRecorder()
	handler.ServeHTTP(uiResponse, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusAccepted, apiResponse.Code)
	require.Equal(t, http.StatusCreated, uiResponse.Code)
}

func TestEndpointsRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()
	handler := httpserver.New(&fakeReadiness{}, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
	require.Equal(t, http.MethodGet, response.Header().Get("Allow"))
}

type fakeReadiness struct {
	err    error
	called bool
}

func (f *fakeReadiness) Ready(context.Context) error {
	f.called = true
	return f.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}
