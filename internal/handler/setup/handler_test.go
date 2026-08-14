package setup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	setuphandler "github.com/blackpearl-media/blackpearl/internal/handler/setup"
	setupservice "github.com/blackpearl-media/blackpearl/internal/service/setup"
	"github.com/stretchr/testify/require"
)

func TestHandlerStatusReturnsPublicStateAndCSRFWithNoStoreHeaders(t *testing.T) {
	t.Parallel()
	service := &fakeService{status: setupservice.Status{SetupRequired: true, TokenConfigured: false}}
	handler, err := setuphandler.New(service)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8082/api/setup/status", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, "no-referrer", response.Header().Get("Referrer-Policy"))
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, true, body["setupRequired"])
	require.NotEmpty(t, body["csrfToken"])
	require.NotContains(t, response.Body.String(), "torbox.token")
}

func TestHandlerDiscoverRequiresLoopbackOriginAndCSRF(t *testing.T) {
	t.Parallel()
	service := &fakeService{items: []domain.MediaCandidate{{ObjectID: "17:3", Name: "Example.mkv", Extension: ".mkv", Size: 12}}}
	handler, err := setuphandler.New(service)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)

	request := httptest.NewRequest(http.MethodPost, "http://localhost:8082/api/setup/discover", bytes.NewBufferString(`{"token":"private-token"}`))
	request.Host = "localhost:8082"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:8082")
	request.Header.Set("X-BlackPearl-CSRF", csrf)
	request.Header.Set("X-BlackPearl-Session", "session-value")
	request.Header.Set("X-BlackPearl-Bootstrap", "bootstrap-value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "Example.mkv")
	require.NotContains(t, response.Body.String(), "private-token")
	require.Equal(t, "private-token", service.discoverToken)
	require.Equal(t, "session-value", service.authorizeSession)
	require.Equal(t, "bootstrap-value", service.authorizeBootstrap)
	require.Len(t, response.Header().Get("X-BlackPearl-Session"), 64)
	require.Empty(t, response.Header().Get("Set-Cookie"))

	badOrigin := httptest.NewRequest(http.MethodPost, "http://localhost:8082/api/setup/discover", bytes.NewBufferString(`{}`))
	badOrigin.Host = "localhost:8082"
	badOrigin.Header.Set("Origin", "https://evil.example")
	badOrigin.Header.Set("X-BlackPearl-CSRF", csrf)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, badOrigin)
	require.Equal(t, http.StatusForbidden, denied.Code)
}

func TestHandlerConfigurationRejectsUnknownFieldsAndMapsInvalidSelection(t *testing.T) {
	t.Parallel()
	service := &fakeService{applyErr: setupservice.ErrInvalidSelection}
	handler, err := setuphandler.New(service)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)

	unknown := newMutation(t, http.MethodPut, "/api/setup/configuration", csrf, `{"objectId":"17:3","title":"Example","year":2026,"extra":true}`)
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	require.Equal(t, http.StatusBadRequest, unknownResponse.Code)

	valid := newMutation(t, http.MethodPut, "/api/setup/configuration", csrf, `{"objectId":"17:3","title":"Example","year":2026}`)
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, valid)
	require.Equal(t, http.StatusUnprocessableEntity, validResponse.Code)
	require.Contains(t, validResponse.Body.String(), "invalid_selection")
}

func TestHandlerNeverEchoesTokenOnProviderFailure(t *testing.T) {
	t.Parallel()
	service := &fakeService{discoverErr: errors.New("upstream includes private-token")}
	handler, err := setuphandler.New(service)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPost, "/api/setup/discover", csrf, `{"token":"private-token"}`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "private-token")
}

type fakeService struct {
	status             setupservice.Status
	items              []domain.MediaCandidate
	discoverToken      string
	discoverErr        error
	selected           domain.SetupConfiguration
	applyErr           error
	authorizeErr       error
	authorizeSession   string
	authorizeBootstrap string
}

func (f *fakeService) Status() setupservice.Status { return f.status }
func (f *fakeService) Discover(_ context.Context, token string) ([]domain.MediaCandidate, error) {
	f.discoverToken = token
	return f.items, f.discoverErr
}
func (f *fakeService) Apply(context.Context, setupservice.ApplyRequest) (domain.SetupConfiguration, error) {
	return f.selected, f.applyErr
}
func (f *fakeService) AuthorizeSetup(_ context.Context, _ string, session string, bootstrap string) error {
	f.authorizeSession = session
	f.authorizeBootstrap = bootstrap
	return f.authorizeErr
}
func (*fakeService) IssueSession(context.Context, string) (string, error) {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil
}

func fetchCSRF(t *testing.T, handler http.Handler) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8082/api/setup/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotEmpty(t, body.CSRFToken)
	return body.CSRFToken
}

func newMutation(t *testing.T, method string, path string, csrf string, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "http://localhost:8082"+path, bytes.NewBufferString(body))
	request.Host = "localhost:8082"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:8082")
	request.Header.Set("X-BlackPearl-CSRF", csrf)
	return request
}
