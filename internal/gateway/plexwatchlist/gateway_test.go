package plexwatchlist_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/plexwatchlist"
	"github.com/stretchr/testify/require"
)

type staticTokenSource struct {
	token string
	err   error
}

func (s staticTokenSource) Token(context.Context) (string, error) { return s.token, s.err }

func TestSnapshotUsesHeaderAuthAndReturnsBoundedPaginatedIntent(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		require.Equal(t, "/base/library/sections/watchlist/all", request.URL.Path)
		require.Equal(t, "private-token", request.Header.Get("X-Plex-Token"))
		require.Equal(t, "application/json", request.Header.Get("Accept"))
		require.Equal(t, "1", request.URL.Query().Get("includeAdvanced"))
		require.Equal(t, "1", request.URL.Query().Get("includeMeta"))
		require.Equal(t, "2", request.URL.Query().Get("X-Plex-Container-Size"))
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("X-Plex-Container-Start") {
		case "0":
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":2,"totalSize":4,"Metadata":[{"guid":"plex://movie/one","type":"movie","title":"First Movie","year":2024},{"guid":"plex://show/two","type":"show","title":"Example Show","year":2020}]}}`))
			require.NoError(t, err)
		case "2":
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":2,"totalSize":4,"Metadata":[{"guid":"plex://movie/one","type":"movie","title":"Duplicate","year":2024},{"guid":"plex://movie/three","type":"movie","title":"Third Movie","year":2026},{"guid":"bad","type":"season","title":"Ignored","year":2026}]}}`))
			require.NoError(t, err)
		default:
			http.Error(writer, "unexpected page", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	gateway, err := plexwatchlist.New(plexwatchlist.Options{BaseURL: server.URL + "/base/", PageSize: 2, MaximumItems: 10}, staticTokenSource{token: "private-token"}, server.Client())
	require.NoError(t, err)

	items, err := gateway.Snapshot(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, requests)
	require.Len(t, items, 3)
	require.Equal(t, "First Movie", items[0].Title())
	require.Equal(t, acquisition.WatchlistMediaTypeShow, items[1].MediaType())
	require.Equal(t, "Third Movie", items[2].Title())
}

func TestSnapshotMapsAuthenticationAndSanitizesProviderFailures(t *testing.T) {
	t.Parallel()
	secret := "private-watchlist-token"
	tests := []struct {
		name       string
		handler    http.Handler
		tokenError error
		want       error
	}{
		{name: "unauthorized", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { http.Error(writer, secret, http.StatusUnauthorized) }), want: domain.ErrUnauthorized},
		{name: "provider error", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { http.Error(writer, secret, http.StatusBadGateway) }), want: plexwatchlist.ErrUnavailable},
		{name: "oversize body", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(make([]byte, 2*1024*1024+1)) }), want: plexwatchlist.ErrUnavailable},
		{name: "token source", handler: http.NotFoundHandler(), tokenError: errors.New(secret), want: plexwatchlist.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(test.handler)
			t.Cleanup(server.Close)
			gateway, err := plexwatchlist.New(plexwatchlist.Options{BaseURL: server.URL}, staticTokenSource{token: "token", err: test.tokenError}, server.Client())
			require.NoError(t, err)

			_, err = gateway.Snapshot(context.Background())

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), server.URL)
		})
	}
}

func TestSnapshotRejectsRedirectsAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.Handler
		context func() context.Context
	}{
		{name: "redirect", handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "https://example.test/", http.StatusFound)
		}), context: context.Background},
		{name: "cancelled", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(100 * time.Millisecond) }), context: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(test.handler)
			t.Cleanup(server.Close)
			gateway, err := plexwatchlist.New(plexwatchlist.Options{BaseURL: server.URL}, staticTokenSource{token: "token"}, server.Client())
			require.NoError(t, err)

			_, err = gateway.Snapshot(test.context())

			require.Error(t, err)
			if test.name == "cancelled" {
				require.True(t, errors.Is(err, context.Canceled), fmt.Sprintf("expected cancellation, got %v", err))
			} else {
				require.ErrorIs(t, err, plexwatchlist.ErrUnavailable)
			}
		})
	}
}

func TestNewRejectsInvalidGatewayConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options plexwatchlist.Options
		tokens  plexwatchlist.TokenSource
		client  *http.Client
	}{
		{name: "relative URL", options: plexwatchlist.Options{BaseURL: "plex"}, tokens: staticTokenSource{token: "token"}, client: http.DefaultClient},
		{name: "credentials in URL", options: plexwatchlist.Options{BaseURL: "https://user:secret@plex.test"}, tokens: staticTokenSource{token: "token"}, client: http.DefaultClient},
		{name: "query in URL", options: plexwatchlist.Options{BaseURL: "https://plex.test?secret=x"}, tokens: staticTokenSource{token: "token"}, client: http.DefaultClient},
		{name: "nil tokens", options: plexwatchlist.Options{BaseURL: "https://plex.test"}, client: http.DefaultClient},
		{name: "nil client", options: plexwatchlist.Options{BaseURL: "https://plex.test"}, tokens: staticTokenSource{token: "token"}},
		{name: "page size", options: plexwatchlist.Options{BaseURL: "https://plex.test", PageSize: 101}, tokens: staticTokenSource{token: "token"}, client: http.DefaultClient},
		{name: "maximum items", options: plexwatchlist.Options{BaseURL: "https://plex.test", MaximumItems: 1001}, tokens: staticTokenSource{token: "token"}, client: http.DefaultClient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := plexwatchlist.New(test.options, test.tokens, test.client)
			require.Error(t, err)
		})
	}
}
