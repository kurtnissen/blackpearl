package plexmetadata_test

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
	"github.com/blackpearl-media/blackpearl/internal/gateway/plexmetadata"
	"github.com/stretchr/testify/require"
)

const metadataToken = "private-plex-token"

func TestGatewayNextResolvesSameSeasonGapAndSeasonTransition(t *testing.T) {
	t.Parallel()
	showID := "5d9c086ce98e47001eb0f520"
	seasonOneID := "5d9c09de2192ba001f32230f"
	seasonTwoID := "602e67f3535238002c35ccb4"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, metadataToken, request.Header.Get("X-Plex-Token"))
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/library/metadata/" + showID + "/children":
			fmt.Fprintf(response, `{"MediaContainer":{"size":3,"Metadata":[{"type":"season","index":2,"ratingKey":%q},{"type":"season","index":0,"ratingKey":"000000000000000000000000"},{"type":"season","index":1,"ratingKey":%q}]}}`, seasonTwoID, seasonOneID)
		case "/library/metadata/" + seasonOneID + "/children":
			fmt.Fprint(response, `{"MediaContainer":{"size":3,"Metadata":[{"type":"episode","parentIndex":1,"index":8},{"type":"episode","parentIndex":1,"index":3},{"type":"episode","parentIndex":1,"index":1}]}}`)
		case "/library/metadata/" + seasonTwoID + "/children":
			fmt.Fprint(response, `{"MediaContainer":{"size":2,"Metadata":[{"type":"episode","parentIndex":2,"index":2},{"type":"episode","parentIndex":2,"index":1}]}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	gateway := newMetadataGateway(t, server.URL, &metadataTokenSource{token: metadataToken})

	nextGap, err := gateway.Next(context.Background(), "plex://show/"+showID, mustCoordinate(t, 1, 1))
	require.NoError(t, err)
	require.Equal(t, 1, nextGap.Season())
	require.Equal(t, 3, nextGap.Episode())

	nextSeason, err := gateway.Next(context.Background(), "plex://show/"+showID, mustCoordinate(t, 1, 8))
	require.NoError(t, err)
	require.Equal(t, 2, nextSeason.Season())
	require.Equal(t, 1, nextSeason.Episode())
}

func TestGatewayNextReturnsNotFoundForTerminalOrUnknownCurrentSeason(t *testing.T) {
	t.Parallel()
	showID := "5d9c086ce98e47001eb0f520"
	seasonID := "5d9c09de2192ba001f32230f"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/library/metadata/" + showID + "/children":
			fmt.Fprintf(response, `{"MediaContainer":{"size":1,"Metadata":[{"type":"season","index":1,"ratingKey":%q}]}}`, seasonID)
		case "/library/metadata/" + seasonID + "/children":
			fmt.Fprint(response, `{"MediaContainer":{"size":2,"Metadata":[{"type":"episode","parentIndex":1,"index":1},{"type":"episode","parentIndex":1,"index":2}]}}`)
		}
	}))
	t.Cleanup(server.Close)
	gateway := newMetadataGateway(t, server.URL, &metadataTokenSource{token: metadataToken})

	_, err := gateway.Next(context.Background(), "plex://show/"+showID, mustCoordinate(t, 1, 2))
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, err = gateway.Next(context.Background(), "plex://show/"+showID, mustCoordinate(t, 2, 1))
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestGatewayNextRejectsMalformedIdentityAndFailsClosedAtHTTPBoundary(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, `{"MediaContainer":{"size":2,"Metadata":[]}}`)
	}))
	t.Cleanup(server.Close)
	gateway := newMetadataGateway(t, server.URL, &metadataTokenSource{token: metadataToken})

	_, err := gateway.Next(context.Background(), "plex://movie/5d9c086ce98e47001eb0f520", mustCoordinate(t, 1, 1))
	require.Error(t, err)
	require.NotErrorIs(t, err, plexmetadata.ErrUnavailable)
	_, err = gateway.Next(context.Background(), "plex://show/UPPERCASE00000000000000", mustCoordinate(t, 1, 1))
	require.Error(t, err)

	_, err = gateway.Next(context.Background(), "plex://show/5d9c086ce98e47001eb0f520", mustCoordinate(t, 1, 1))
	require.ErrorIs(t, err, plexmetadata.ErrUnavailable)
	require.NotContains(t, err.Error(), metadataToken)
}

func TestGatewayNextRejectsOversizedMetadata(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, strings.Repeat("x", (2<<20)+1))
	}))
	t.Cleanup(server.Close)
	gateway := newMetadataGateway(t, server.URL, &metadataTokenSource{token: metadataToken})

	_, err := gateway.Next(context.Background(), "plex://show/5d9c086ce98e47001eb0f520", mustCoordinate(t, 1, 1))

	require.ErrorIs(t, err, plexmetadata.ErrUnavailable)
}

func TestGatewayNextMapsAuthenticationCancellationAndTokenFailures(t *testing.T) {
	t.Parallel()
	showID := "5d9c086ce98e47001eb0f520"
	for _, test := range []struct {
		name   string
		status int
		tokens *metadataTokenSource
		cancel bool
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, tokens: &metadataTokenSource{token: metadataToken}, want: domain.ErrUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, tokens: &metadataTokenSource{token: metadataToken}, want: domain.ErrUnauthorized},
		{name: "temporary", status: http.StatusBadGateway, tokens: &metadataTokenSource{token: metadataToken}, want: plexmetadata.ErrUnavailable},
		{name: "token", status: http.StatusOK, tokens: &metadataTokenSource{err: errors.New("private token path")}, want: plexmetadata.ErrUnavailable},
		{name: "canceled", status: http.StatusOK, tokens: &metadataTokenSource{token: metadataToken}, cancel: true, want: context.Canceled},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				fmt.Fprint(response, `private response body`)
			}))
			t.Cleanup(server.Close)
			gateway := newMetadataGateway(t, server.URL, test.tokens)
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			_, err := gateway.Next(ctx, "plex://show/"+showID, mustCoordinate(t, 1, 1))

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), metadataToken)
			require.NotContains(t, err.Error(), "private")
		})
	}
}

func TestGatewayNextRefusesRedirects(t *testing.T) {
	t.Parallel()
	var followed atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	t.Cleanup(receiver.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, receiver.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	gateway := newMetadataGateway(t, redirector.URL, &metadataTokenSource{token: metadataToken})

	_, err := gateway.Next(context.Background(), "plex://show/5d9c086ce98e47001eb0f520", mustCoordinate(t, 1, 1))

	require.ErrorIs(t, err, plexmetadata.ErrUnavailable)
	require.Zero(t, followed.Load())
}

func TestNewGatewayRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	client := &http.Client{}
	for _, endpoint := range []string{"metadata.provider.plex.tv", "https://user:pass@metadata.provider.plex.tv", "https://metadata.provider.plex.tv?token=secret"} {
		_, err := plexmetadata.New(endpoint, &metadataTokenSource{token: metadataToken}, client)
		require.Error(t, err)
	}
	_, err := plexmetadata.New("https://metadata.provider.plex.tv", nil, client)
	require.Error(t, err)
	_, err = plexmetadata.New("https://metadata.provider.plex.tv", &metadataTokenSource{token: metadataToken}, nil)
	require.Error(t, err)
}

func mustCoordinate(t *testing.T, season int, episode int) domain.EpisodeCoordinate {
	t.Helper()
	coordinate, err := domain.NewEpisodeCoordinate(season, episode)
	require.NoError(t, err)
	return coordinate
}

func newMetadataGateway(t *testing.T, baseURL string, tokens *metadataTokenSource) *plexmetadata.Gateway {
	t.Helper()
	gateway, err := plexmetadata.New(baseURL, tokens, &http.Client{})
	require.NoError(t, err)
	return gateway
}

type metadataTokenSource struct {
	token string
	err   error
}

func (s *metadataTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.token, s.err
}
