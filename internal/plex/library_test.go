package plex_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/plex"
	"github.com/stretchr/testify/require"
)

type staticLibraryTokenSource struct {
	token string
	err   error
}

func TestLibraryRefresherRejectsUnsafeOrMalformedSectionResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		body      string
		token     string
		tokenErr  error
		want      string
		forbidden string
		wantCalls int
	}{
		{name: "missing match", status: http.StatusOK, body: `{"MediaContainer":{"size":1,"Directory":[{"key":"9","Location":[{"path":"/other"}]}]}}`, token: "token", want: "no matching", wantCalls: 1},
		{name: "malformed JSON", status: http.StatusOK, body: `{`, token: "token", want: "decode Plex", wantCalls: 1},
		{name: "invalid count", status: http.StatusOK, body: `{"MediaContainer":{"size":999,"Directory":[{"key":"2","Location":[{"path":"/blackpearl/Movies"}]}]}}`, token: "token", want: "validate Plex", wantCalls: 1},
		{name: "unsafe section key", status: http.StatusOK, body: `{"MediaContainer":{"size":1,"Directory":[{"key":"../private","Location":[{"path":"/blackpearl/Movies"}]}]}}`, token: "token", want: "validate Plex", wantCalls: 1},
		{name: "unauthorized body is private", status: http.StatusUnauthorized, body: `reflected plex-secret`, token: "plex-secret", want: "status 401", forbidden: "plex-secret", wantCalls: 1},
		{name: "invalid token", status: http.StatusOK, body: `{}`, token: " token ", want: "validate Plex refresh credential", forbidden: "token", wantCalls: 0},
		{name: "token source failure", status: http.StatusOK, body: `{}`, tokenErr: errors.New("private credential path"), want: "load Plex refresh credential", forbidden: "private credential path", wantCalls: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls++
				writer.WriteHeader(test.status)
				_, err := writer.Write([]byte(test.body))
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			refresher, err := plex.NewLibraryRefresher(
				server.URL,
				staticLibraryTokenSource{token: test.token, err: test.tokenErr},
				[]string{"/blackpearl/Movies"},
				server.Client(),
			)
			require.NoError(t, err)

			err = refresher.Refresh(context.Background())

			require.ErrorContains(t, err, test.want)
			if test.forbidden != "" {
				require.NotContains(t, err.Error(), test.forbidden)
			}
			require.Equal(t, test.wantCalls, calls)
		})
	}
}

func TestLibraryRefresherBoundsSectionResponseAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	t.Run("oversized", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":0,"padding":"` + strings.Repeat("x", 2<<20) + `"}}`))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		refresher, err := plex.NewLibraryRefresher(server.URL, staticLibraryTokenSource{token: "token"}, []string{"/blackpearl/Movies"}, server.Client())
		require.NoError(t, err)

		err = refresher.Refresh(context.Background())

		require.ErrorContains(t, err, "read Plex")
	})
	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(server.Close)
		refresher, err := plex.NewLibraryRefresher(server.URL, staticLibraryTokenSource{token: "token"}, []string{"/blackpearl/Movies"}, server.Client())
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err = refresher.Refresh(ctx)

		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestLibraryRefresherDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	t.Parallel()
	redirected := false
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusFound)
	}))
	t.Cleanup(source.Close)
	client := source.Client()
	client.Transport = destination.Client().Transport
	refresher, err := plex.NewLibraryRefresher(source.URL, staticLibraryTokenSource{token: "plex-secret"}, []string{"/blackpearl/Movies"}, client)
	require.NoError(t, err)

	err = refresher.Refresh(context.Background())

	require.ErrorContains(t, err, "status 302")
	require.False(t, redirected)
	require.NotContains(t, err.Error(), "plex-secret")
}

func TestNewLibraryRefresherRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		url    string
		tokens plex.TokenSource
		roots  []string
		client *http.Client
	}{
		{name: "relative URL", url: "plex", tokens: staticLibraryTokenSource{token: "token"}, roots: []string{"/blackpearl/Movies"}, client: http.DefaultClient},
		{name: "URL credentials", url: "http://user:pass@plex", tokens: staticLibraryTokenSource{token: "token"}, roots: []string{"/blackpearl/Movies"}, client: http.DefaultClient},
		{name: "nil token source", url: "http://plex", roots: []string{"/blackpearl/Movies"}, client: http.DefaultClient},
		{name: "nil client", url: "http://plex", tokens: staticLibraryTokenSource{token: "token"}, roots: []string{"/blackpearl/Movies"}},
		{name: "empty roots", url: "http://plex", tokens: staticLibraryTokenSource{token: "token"}, client: http.DefaultClient},
		{name: "relative root", url: "http://plex", tokens: staticLibraryTokenSource{token: "token"}, roots: []string{"blackpearl/Movies"}, client: http.DefaultClient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := plex.NewLibraryRefresher(test.url, test.tokens, test.roots, test.client)
			require.Error(t, err)
		})
	}
}

func (s staticLibraryTokenSource) Token(context.Context) (string, error) {
	return s.token, s.err
}

func TestLibraryRefresherRefreshesOnlyExactBlackPearlRoots(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	requested := make([]string, 0, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "plex-secret", request.Header.Get("X-Plex-Token"))
		mutex.Lock()
		requested = append(requested, request.URL.EscapedPath())
		mutex.Unlock()
		switch request.URL.EscapedPath() {
		case "/library/sections":
			require.Equal(t, "application/json", request.Header.Get("Accept"))
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":4,"Directory":[{"key":"2","type":"movie","title":"BlackPearl","Location":[{"id":2,"path":"/blackpearl/Movies"}]},{"key":"3","type":"show","title":"BlackPearl TV","Location":[{"id":3,"path":"/blackpearl/TV Shows"}]},{"key":"4","type":"movie","title":"Other","Location":[{"id":4,"path":"/media/Movies"}]},{"key":"5","type":"movie","title":"Prefix Trap","Location":[{"id":5,"path":"/blackpearl/Movies Extra"}]}]}}`))
			require.NoError(t, err)
		case "/library/sections/2/refresh", "/library/sections/3/refresh":
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	refresher, err := plex.NewLibraryRefresher(
		server.URL,
		staticLibraryTokenSource{token: "plex-secret"},
		[]string{"/blackpearl/Movies", "/blackpearl/TV Shows"},
		server.Client(),
	)
	require.NoError(t, err)

	err = refresher.Refresh(context.Background())

	require.NoError(t, err)
	mutex.Lock()
	paths := append([]string(nil), requested...)
	mutex.Unlock()
	slices.Sort(paths)
	require.Equal(t, []string{
		"/library/sections",
		"/library/sections/2/refresh",
		"/library/sections/3/refresh",
	}, paths)
}
