package plex_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/plex"
	"github.com/stretchr/testify/require"
)

func TestRefreshUsesHeaderTokenAndEscapedSectionPath(t *testing.T) {
	t.Parallel()
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedURL = request.URL.RequestURI()
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "top-secret", request.Header.Get("X-Plex-Token"))
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	gateway, err := plex.New(server.URL, "top-secret", "section 7", server.Client())
	require.NoError(t, err)

	err = gateway.Refresh(context.Background())

	require.NoError(t, err)
	require.Equal(t, "/library/sections/section%207/refresh", receivedURL)
	require.NotContains(t, receivedURL, "top-secret")
}

func TestRefreshReturnsBoundedContextualStatusError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, err := writer.Write(make([]byte, 10_000))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := plex.New(server.URL, "token", "1", server.Client())
	require.NoError(t, err)

	err = gateway.Refresh(context.Background())

	require.ErrorContains(t, err, "status 502")
	require.Less(t, len(err.Error()), 5_000)
}

func TestRefreshHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	t.Cleanup(server.Close)
	gateway, err := plex.New(server.URL, "token", "1", server.Client())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = gateway.Refresh(ctx)

	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestNewRejectsIncompleteOrInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		token     string
		sectionID string
		client    *http.Client
	}{
		{name: "relative URL", baseURL: "plex", token: "token", sectionID: "1", client: http.DefaultClient},
		{name: "empty token", baseURL: "http://plex", sectionID: "1", client: http.DefaultClient},
		{name: "empty section", baseURL: "http://plex", token: "token", client: http.DefaultClient},
		{name: "nil client", baseURL: "http://plex", token: "token", sectionID: "1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := plex.New(test.baseURL, test.token, test.sectionID, test.client)
			require.Error(t, err)
		})
	}
}
