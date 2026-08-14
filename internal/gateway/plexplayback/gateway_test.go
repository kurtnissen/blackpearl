package plexplayback_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/plexplayback"
	"github.com/stretchr/testify/require"
)

const playbackToken = "private-plex-token"

func TestGatewaySnapshotReturnsOnlyNormalizedBlackPearlEpisodes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/status/sessions", request.URL.Path)
		require.Equal(t, "application/json", request.Header.Get("Accept"))
		require.Equal(t, playbackToken, request.Header.Get("X-Plex-Token"))
		response.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprint(response, `{"MediaContainer":{"size":3,"Metadata":[`+
			playbackEpisodeJSON("paused", "/blackpearl/TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4", true)+`,`+
			`{"type":"movie","viewOffset":254000,"duration":2186773,"Player":{"state":"playing"},"Media":[{"Part":[{"file":"/blackpearl/Movies/Film (2026)/Film (2026).mp4","selected":true}]}]},`+
			playbackEpisodeJSON("playing", "/other/TV Shows/MariposaHD (2006)/Season 01/episode.mp4", true)+`]}}`)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway := newPlaybackGateway(t, server.URL, &fakeTokenSource{token: playbackToken})

	actual, err := gateway.Snapshot(context.Background())

	require.NoError(t, err)
	require.Len(t, actual, 1)
	require.Equal(t, "plex://show/5d9c086ce98e47001eb0f520", actual[0].ExternalShowID())
	require.Equal(t, "TV Shows/MariposaHD (2006)/Season 01/MariposaHD (2006) - S01E01 - Episode 1.mp4", actual[0].VirtualPath())
	require.Equal(t, 1, actual[0].Coordinate().Season())
	require.Equal(t, 1, actual[0].Coordinate().Episode())
	require.Equal(t, domain.PlaybackStatePaused, actual[0].State())
}

func TestGatewaySnapshotIsolatesMalformedSessions(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, err := fmt.Fprint(response, `{"MediaContainer":{"size":4,"Metadata":[`+
			playbackEpisodeJSON("buffering", "/blackpearl/TV Shows/Show (2026)/Season 01/bad.mp4", true)+`,`+
			playbackEpisodeJSON("playing", "/blackpearl/TV Shows/Show (2026)/Season 01/unselected.mp4", false)+`,`+
			strings.Replace(playbackEpisodeJSON("playing", "/blackpearl/TV Shows/Show (2026)/Season 01/duplicate.mp4", true), `"Part":[`, `"Part":[{"file":"/blackpearl/TV Shows/Show (2026)/Season 01/other.mp4","selected":true},`, 1)+`,`+
			playbackEpisodeJSON("playing", "/blackpearl/TV Shows/MariposaHD (2006)/Season 01/valid.mp4", true)+`]}}`)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway := newPlaybackGateway(t, server.URL, &fakeTokenSource{token: playbackToken})

	actual, err := gateway.Snapshot(context.Background())

	require.NoError(t, err)
	require.Len(t, actual, 1)
	require.Equal(t, "TV Shows/MariposaHD (2006)/Season 01/valid.mp4", actual[0].VirtualPath())
}

func TestGatewaySnapshotFailsClosedAtHTTPBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		body   string
		token  *fakeTokenSource
		want   error
		cancel bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"private":"response"}`, token: &fakeTokenSource{token: playbackToken}, want: domain.ErrUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, token: &fakeTokenSource{token: playbackToken}, want: domain.ErrUnauthorized},
		{name: "provider failure", status: http.StatusBadGateway, body: `private-provider-body`, token: &fakeTokenSource{token: playbackToken}, want: plexplayback.ErrUnavailable},
		{name: "malformed envelope", status: http.StatusOK, body: `{"MediaContainer":{"size":2,"Metadata":[]}}`, token: &fakeTokenSource{token: playbackToken}, want: plexplayback.ErrUnavailable},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", (2<<20)+1), token: &fakeTokenSource{token: playbackToken}, want: plexplayback.ErrUnavailable},
		{name: "token unavailable", status: http.StatusOK, token: &fakeTokenSource{err: errors.New("private token path")}, want: plexplayback.ErrUnavailable},
		{name: "canceled", status: http.StatusOK, token: &fakeTokenSource{token: playbackToken}, want: context.Canceled, cancel: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, writeErr := fmt.Fprint(response, test.body)
				require.NoError(t, writeErr)
			}))
			t.Cleanup(server.Close)
			gateway := newPlaybackGateway(t, server.URL, test.token)
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			_, err := gateway.Snapshot(ctx)

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), playbackToken)
			require.NotContains(t, err.Error(), "private")
		})
	}
}

func TestGatewaySnapshotRefusesRedirectsWithoutForwardingCredential(t *testing.T) {
	t.Parallel()
	var leaked atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	t.Cleanup(receiver.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Redirect(response, &http.Request{}, receiver.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	gateway := newPlaybackGateway(t, redirector.URL, &fakeTokenSource{token: playbackToken})

	_, err := gateway.Snapshot(context.Background())

	require.ErrorIs(t, err, plexplayback.ErrUnavailable)
	require.Zero(t, leaked.Load())
}

func TestNewGatewayRejectsUnsafeDependencies(t *testing.T) {
	t.Parallel()
	client := &http.Client{}
	for _, options := range []plexplayback.Options{
		{BaseURL: "plex:32400", LibraryRoot: "/blackpearl"},
		{BaseURL: "http://user:pass@plex:32400", LibraryRoot: "/blackpearl"},
		{BaseURL: "http://plex:32400?token=secret", LibraryRoot: "/blackpearl"},
		{BaseURL: "http://plex:32400", LibraryRoot: "/"},
		{BaseURL: "http://plex:32400", LibraryRoot: "blackpearl"},
	} {
		_, err := plexplayback.New(options, &fakeTokenSource{token: playbackToken}, client)
		require.Error(t, err)
	}
	_, err := plexplayback.New(plexplayback.Options{BaseURL: "http://plex:32400", LibraryRoot: "/blackpearl"}, nil, client)
	require.Error(t, err)
	_, err = plexplayback.New(plexplayback.Options{BaseURL: "http://plex:32400", LibraryRoot: "/blackpearl"}, &fakeTokenSource{token: playbackToken}, nil)
	require.Error(t, err)
}

func playbackEpisodeJSON(state string, file string, selected bool) string {
	return fmt.Sprintf(`{"type":"episode","grandparentGuid":"plex://show/5d9c086ce98e47001eb0f520","parentIndex":1,"index":1,"viewOffset":254000,"duration":2186773,"Player":{"state":%q},"Media":[{"Part":[{"file":%q,"selected":%t}]}]}`, state, file, selected)
}

func newPlaybackGateway(t *testing.T, baseURL string, tokens *fakeTokenSource) *plexplayback.Gateway {
	t.Helper()
	gateway, err := plexplayback.New(plexplayback.Options{BaseURL: baseURL, LibraryRoot: "/blackpearl"}, tokens, &http.Client{})
	require.NoError(t, err)
	return gateway
}

type fakeTokenSource struct {
	token string
	err   error
}

func (s *fakeTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.token, s.err
}
