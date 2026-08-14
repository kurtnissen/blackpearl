package torbox

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGatewayDiscoverReturnsOnlyEligibleVideosInStableOrder(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/api/torrents/mylist", request.URL.Path)
		require.Equal(t, "false", request.URL.Query().Get("bypass_cache"))
		require.Equal(t, "1000", request.URL.Query().Get("limit"))
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		writeEnvelope(writer, true, "ok", `[
			{"id":17,"download_finished":true,"download_present":true,"files":[
				{"id":3,"name":"Zulu.MKV","size":20,"hash":"hash-z"},
				{"id":4,"name":"alpha.mp4","size":10,"md5":"hash-a"},
				{"id":5,"name":"sample.mkv","size":1,"hash":"hash-s"},
				{"id":6,"name":"subs.srt","size":1,"hash":"hash-sub"},
				{"id":7,"name":"infected.mp4","size":1,"hash":"hash-i","infected":true},
				{"id":8,"name":"zipped.mp4","size":1,"hash":"hash-zip","zipped":true},
				{"id":9,"name":"empty.mp4","size":0,"hash":"hash-e"},
				{"id":10,"name":"hashless.mp4","size":1}
			]},
			{"id":18,"download_finished":false,"download_present":true,"files":[{"id":1,"name":"unfinished.mp4","size":1,"hash":"h"}]},
			{"id":19,"download_finished":true,"download_present":false,"files":[{"id":1,"name":"absent.mkv","size":1,"hash":"h"}]}
		]`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	candidates, err := gateway.Discover(context.Background())

	require.NoError(t, err)
	require.Equal(t, []domain.MediaCandidate{
		{Provider: providerName, ObjectID: "17:4", Name: "alpha.mp4", Extension: ".mp4", Size: 10},
		{Provider: providerName, ObjectID: "17:3", Name: "Zulu.MKV", Extension: ".mkv", Size: 20},
	}, candidates)
}

func TestGatewayDiscoverDoesNotRequestMediaBytesOrDownloadLinks(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/api/torrents/mylist" {
			t.Fatalf("unexpected discovery request: %s", request.URL.Path)
		}
		writeEnvelope(writer, true, "ok", `[]`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	_, err := gateway.Discover(context.Background())

	require.NoError(t, err)
}

func TestGatewayDiscoverRedactsTokenFromProviderFailure(t *testing.T) {
	t.Parallel()
	secret := "discovery-secret"
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeEnvelope(writer, false, "denied "+secret+" https://signed.example/file?token="+secret, `null`)
	})
	gateway, err := New(Options{APIBaseURL: api.URL + "/v1/api/", APIToken: secret, MetadataTTL: time.Minute, LinkTTL: time.Hour}, api.Client())
	require.NoError(t, err)

	_, err = gateway.Discover(context.Background())

	require.ErrorContains(t, err, "denied")
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, err.Error(), "signed.example")
}

func TestGatewayDiscoverHonorsCancellation(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeEnvelope(writer, true, "ok", `[]`)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gateway.Discover(ctx)

	require.ErrorIs(t, err, context.Canceled)
}

func TestGatewayDiscoverPreservesProviderAuthenticationFailure(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "denied", http.StatusUnauthorized)
	})
	gateway := newTestGateway(t, api.URL+"/v1/api/", api.Client())

	_, err := gateway.Discover(context.Background())

	require.ErrorIs(t, err, domain.ErrUnauthorized)
}
