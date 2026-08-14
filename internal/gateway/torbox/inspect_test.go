package torbox

import (
	"context"
	"net/http"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayInspectCreatedTorrentReturnsEligibleMedia(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v1/api/torrents/mylist", request.URL.Path)
		require.Equal(t, "17", request.URL.Query().Get("id"))
		require.Equal(t, "true", request.URL.Query().Get("bypass_cache"))
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		writeEnvelope(writer, true, "ok", `{
			"id":17,"download_finished":true,"download_present":true,"files":[
				{"id":4,"name":"Extras/sample.mkv","size":1,"hash":"sample"},
				{"id":3,"name":"Example.Show.S07E02.mkv","size":20,"hash":"video"},
				{"id":2,"name":"Example.Show.S07E01.mp4","size":10,"hash":"video-2"},
				{"id":1,"name":"subtitle.srt","size":1,"hash":"subtitle"}
			]
		}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	created, err := acquisition.NewCreatedObject(providerName, "17")
	require.NoError(t, err)

	items, err := gateway.InspectCreatedTorrent(context.Background(), created)

	require.NoError(t, err)
	require.Equal(t, []domain.MediaCandidate{
		{ObjectID: "17:2", Name: "Example.Show.S07E01.mp4", Extension: ".mp4", Size: 10},
		{ObjectID: "17:3", Name: "Example.Show.S07E02.mkv", Extension: ".mkv", Size: 20},
	}, items)
}

func TestGatewayInspectCreatedTorrentDistinguishesNotReadyAndEmptyMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		data      string
		wantReady bool
	}{
		{name: "unfinished", data: `{"id":17,"download_finished":false,"download_present":true,"files":[]}`},
		{name: "not present", data: `{"id":17,"download_finished":true,"download_present":false,"files":[]}`},
		{name: "ready without video", data: `{"id":17,"download_finished":true,"download_present":true,"files":[{"id":1,"name":"subtitle.srt","size":1,"hash":"subtitle"}]}`, wantReady: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
				writeEnvelope(writer, true, "ok", test.data)
			})
			gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
			created, err := acquisition.NewCreatedObject(providerName, "17")
			require.NoError(t, err)

			items, err := gateway.InspectCreatedTorrent(context.Background(), created)

			if test.wantReady {
				require.NoError(t, err)
				require.Empty(t, items)
				return
			}
			require.ErrorIs(t, err, acquisition.ErrNotReady)
		})
	}
}

func TestGatewayInspectCreatedTorrentValidatesObjectBeforeRequest(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(_ http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected inspect request: %s", request.URL.Path)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	tests := []struct {
		name     string
		provider string
		objectID string
	}{
		{name: "wrong provider", provider: "other", objectID: "17"},
		{name: "compound file ID", provider: providerName, objectID: "17:3"},
		{name: "zero ID", provider: providerName, objectID: "0"},
		{name: "noncanonical ID", provider: providerName, objectID: "017"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			created, err := acquisition.NewCreatedObject(test.provider, test.objectID)
			require.NoError(t, err)

			_, err = gateway.InspectCreatedTorrent(context.Background(), created)

			require.Error(t, err)
		})
	}
}

func TestGatewayInspectCreatedTorrentPreservesNotFoundAuthAndCancellation(t *testing.T) {
	t.Parallel()
	created, err := acquisition.NewCreatedObject(providerName, "17")
	require.NoError(t, err)

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, true, "ok", `{"id":18,"download_finished":true,"download_present":true,"files":[]}`)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		_, err := gateway.InspectCreatedTorrent(context.Background(), created)

		require.ErrorIs(t, err, domain.ErrNotFound)
	})

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "denied", http.StatusForbidden)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

		_, err := gateway.InspectCreatedTorrent(context.Background(), created)

		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, true, "ok", `{"id":17,"download_finished":true,"download_present":true,"files":[]}`)
		})
		gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := gateway.InspectCreatedTorrent(ctx, created)

		require.ErrorIs(t, err, context.Canceled)
	})
}
