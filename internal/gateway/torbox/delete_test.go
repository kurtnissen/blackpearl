package torbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayDeleteCreatedTorrentSendsOneExactControlRequest(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/v1/api/torrents/controltorrent", request.URL.Path)
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		var body struct {
			TorrentID int64  `json:"torrent_id"`
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, int64(17), body.TorrentID)
		require.Equal(t, "delete", body.Operation)
		writeEnvelope(writer, true, "Torrent deleted successfully.", `null`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	err = gateway.DeleteCreatedTorrent(context.Background(), created)

	require.NoError(t, err)
	require.Equal(t, int64(1), requests.Load())
}

func TestGatewayDeleteCreatedTorrentTreatsDefiniteMissingAsClean(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "missing", http.StatusNotFound)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	err = gateway.DeleteCreatedTorrent(context.Background(), created)

	require.NoError(t, err)
}

func TestGatewayDeleteCreatedTorrentRejectsUnsafeInputBeforeIO(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	created, err := acquisition.NewCreatedObject("another-provider", "17")
	require.NoError(t, err)

	err = gateway.DeleteCreatedTorrent(context.Background(), created)

	require.Error(t, err)
	require.Zero(t, requests.Load())
}

func TestGatewayDeleteCreatedTorrentDoesNotRetryAmbiguousFailure(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("ambiguous secret test-token https://signed.invalid")
	})}
	gateway := newTestGateway(t, "https://api.invalid/v1/api/", client)
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	err = gateway.DeleteCreatedTorrent(context.Background(), created)

	require.Error(t, err)
	require.Equal(t, int64(1), requests.Load())
	require.NotContains(t, err.Error(), "test-token")
	require.NotContains(t, err.Error(), "signed.invalid")
}

func TestGatewayDeleteCreatedTorrentPreservesAuthCancellationAndRedirectSafety(t *testing.T) {
	t.Parallel()
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "denied", http.StatusUnauthorized)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		err := gateway.DeleteCreatedTorrent(context.Background(), created)

		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		var requests atomic.Int64
		api := newTestAPI(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := gateway.DeleteCreatedTorrent(ctx, created)

		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, requests.Load())
	})

	t.Run("redirect", func(t *testing.T) {
		t.Parallel()
		var followed atomic.Int64
		destination := newTestAPI(t, func(http.ResponseWriter, *http.Request) { followed.Add(1) })
		api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, http.StatusFound)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		err := gateway.DeleteCreatedTorrent(context.Background(), created)

		require.Error(t, err)
		require.Zero(t, followed.Load())
	})
}

func TestGatewayDeleteCreatedTorrentBoundsAndSanitizesResponse(t *testing.T) {
	t.Parallel()
	created, err := acquisition.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	t.Run("provider detail", func(t *testing.T) {
		t.Parallel()
		secret := "delete-secret"
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, false, "denied "+secret+" https://signed.invalid/file", `null`)
		})
		gateway, gatewayErr := New(Options{
			APIBaseURL: api.URL + "/v1/api/", APIToken: secret, MetadataTTL: 1, LinkTTL: 1,
		}, api.Client())
		require.NoError(t, gatewayErr)

		err := gateway.DeleteCreatedTorrent(context.Background(), created)

		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
		require.NotContains(t, err.Error(), "signed.invalid")
	})

	t.Run("oversized body", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, writeErr := writer.Write([]byte(`{"success":true,"detail":"` + strings.Repeat("x", maximumResponseBody) + `","data":null}`))
			require.NoError(t, writeErr)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		err := gateway.DeleteCreatedTorrent(context.Background(), created)

		require.Error(t, err)
	})
}
