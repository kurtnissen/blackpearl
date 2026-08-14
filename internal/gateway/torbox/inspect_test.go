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
			"id":17,"progress":0.42,"download_finished":true,"download_present":true,"files":[
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

	inspection, err := gateway.InspectCreatedTorrent(context.Background(), created)

	require.NoError(t, err)
	require.Equal(t, 100, inspection.Progress())
	require.Equal(t, []domain.MediaCandidate{
		{ObjectID: "17:2", Name: "Example.Show.S07E01.mp4", Extension: ".mp4", Size: 10},
		{ObjectID: "17:3", Name: "Example.Show.S07E02.mkv", Extension: ".mkv", Size: 20},
	}, inspection.Candidates())
}

func TestGatewayInspectCreatedTorrentDistinguishesNotReadyAndEmptyMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		data         string
		wantReady    bool
		wantProgress int
	}{
		{name: "unfinished", data: `{"id":17,"progress":0.426,"download_finished":false,"download_present":true,"files":[]}`, wantProgress: 42},
		{name: "unfinished provider says complete", data: `{"id":17,"progress":1.0,"download_finished":false,"download_present":true,"files":[]}`, wantProgress: 99},
		{name: "not present", data: `{"id":17,"progress":0.61,"download_finished":true,"download_present":false,"files":[]}`, wantProgress: 61},
		{name: "finished while provider briefly reports stalled", data: `{"id":17,"progress":0.8,"download_state":"stalled","download_finished":true,"download_present":false,"files":[]}`, wantProgress: 80},
		{name: "ready without video", data: `{"id":17,"progress":0.7,"download_finished":true,"download_present":true,"files":[{"id":1,"name":"subtitle.srt","size":1,"hash":"subtitle"}]}`, wantReady: true, wantProgress: 100},
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

			inspection, err := gateway.InspectCreatedTorrent(context.Background(), created)

			require.Equal(t, test.wantProgress, inspection.Progress())
			if test.wantReady {
				require.NoError(t, err)
				require.Empty(t, inspection.Candidates())
				return
			}
			require.ErrorIs(t, err, acquisition.ErrNotReady)
		})
	}
}

func TestGatewayInspectCreatedTorrentReportsTerminalStall(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeEnvelope(writer, true, "ok", `{"id":17,"progress":0.73,"download_state":"stalled (no seeds)","download_finished":false,"download_present":false,"files":[]}`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	created, err := acquisition.NewCreatedObject(providerName, "17")
	require.NoError(t, err)

	inspection, err := gateway.InspectCreatedTorrent(context.Background(), created)

	require.ErrorIs(t, err, acquisition.ErrStalled)
	require.Equal(t, 73, inspection.Progress())
}

func TestGatewayInspectCreatedTorrentRejectsMalformedProgress(t *testing.T) {
	t.Parallel()
	created, err := acquisition.NewCreatedObject(providerName, "17")
	require.NoError(t, err)

	tests := []struct {
		name     string
		progress string
	}{
		{name: "negative", progress: "-0.1"},
		{name: "over one", progress: "1.1"},
		{name: "overflowing number", progress: "1e999"},
		{name: "wrong type", progress: `"half"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
				writeEnvelope(writer, true, "ok", `{"id":17,"progress":`+test.progress+`,"download_finished":false,"download_present":true,"files":[]}`)
			})
			gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

			_, inspectErr := gateway.InspectCreatedTorrent(context.Background(), created)

			require.Error(t, inspectErr)
			require.NotErrorIs(t, inspectErr, acquisition.ErrNotReady)
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

	t.Run("empty provider list", func(t *testing.T) {
		t.Parallel()
		api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeEnvelope(writer, true, "ok", `[]`)
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
