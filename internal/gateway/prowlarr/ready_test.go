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

	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/gateway/prowlarr"
	"github.com/stretchr/testify/require"
)

func TestReadySendsAuthenticatedProbeThroughPathPrefix(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/prowlarr/api/v1/health", request.URL.Path)
		require.Empty(t, request.URL.RawQuery)
		require.Equal(t, "private-key", request.Header.Get("X-Api-Key"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`[]`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL + "/prowlarr/", APIKey: "private-key"}, server.Client())
	require.NoError(t, err)

	err = gateway.Ready(context.Background())

	require.NoError(t, err)
}

func TestReadyMapsAuthenticationAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(server.Close)
		gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
		require.NoError(t, err)

		err = gateway.Ready(context.Background())

		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		var requests atomic.Int64
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			_, err := writer.Write([]byte(`[]`))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: "key"}, server.Client())
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err = gateway.Ready(ctx)

		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, requests.Load())
	})
}

func TestReadyRejectsBoundaryFailuresWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()
	const key = "private-ready-key"
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "server status", handler: func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, err := writer.Write([]byte(key))
			require.NoError(t, err)
		}},
		{name: "redirect", handler: func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "https://secret.invalid/health?key="+key, http.StatusFound)
		}},
		{name: "oversized body", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, err := writer.Write([]byte(strings.Repeat("x", (1<<20)+1)))
			require.NoError(t, err)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(test.handler)
			t.Cleanup(server.Close)
			gateway, err := prowlarr.New(prowlarr.Options{BaseURL: server.URL, APIKey: key}, server.Client())
			require.NoError(t, err)

			err = gateway.Ready(context.Background())

			require.Error(t, err)
			require.NotContains(t, err.Error(), key)
			require.NotContains(t, err.Error(), "secret.invalid")
		})
	}
}

func TestReadySanitizesTransportReadAndCloseFailures(t *testing.T) {
	t.Parallel()
	const secret = "private-ready-failure"
	tests := []struct {
		name      string
		transport roundTripFunc
	}{
		{name: "transport", transport: func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("failed at https://secret.invalid/?key=%s", secret)
		}},
		{name: "read", transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &errorReadCloser{Reader: errorReader{err: errors.New(secret)}}}, nil
		}},
		{name: "close", transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &errorReadCloser{Reader: strings.NewReader(`[]`), err: errors.New(secret)}}, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gateway, err := prowlarr.New(prowlarr.Options{BaseURL: "https://prowlarr.invalid", APIKey: "key"}, &http.Client{Transport: test.transport})
			require.NoError(t, err)

			err = gateway.Ready(context.Background())

			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), "secret.invalid")
		})
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

var _ io.Reader = errorReader{}
