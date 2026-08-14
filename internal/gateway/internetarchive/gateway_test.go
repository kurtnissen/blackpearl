package internetarchive_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/gateway/internetarchive"
	"github.com/stretchr/testify/require"
)

func TestGatewaySearchNormalizesArchiveBitTorrentResults(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/advancedsearch.php", request.URL.Path)
		require.Equal(t, `title:"Tears of Steel" AND title:2012 AND format:"Archive BitTorrent"`, request.URL.Query().Get("q"))
		require.Equal(t, "100", request.URL.Query().Get("rows"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"response":{"docs":[
			{"identifier":"tears-of-steel_202604","title":"Tears of Steel (2012)","item_size":382260309,"btih":"97542f391e9b4c574e721bf95757fe897b0b43fe"},
			{"identifier":"invalid","title":"Invalid","item_size":0,"btih":"not-a-hash"}
		]}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	request, err := acquisition.NewMovieSearch("Tears of Steel", 2012)
	require.NoError(t, err)

	releases, err := gateway.Search(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, releases, 1)
	require.Equal(t, "internet-archive", releases[0].Provider())
	require.Equal(t, "tears-of-steel_202604", releases[0].SourceID())
	require.Equal(t, "Tears of Steel (2012)", releases[0].Title())
	require.Equal(t, int64(382260309), releases[0].Size())
	require.Equal(t, "97542f391e9b4c574e721bf95757fe897b0b43fe", releases[0].InfoHash())
	require.Contains(t, releases[0].MagnetURL(), "urn%3Abtih%3A97542f391e9b4c574e721bf95757fe897b0b43fe")
}

func TestGatewayMaterializesOnlyItsVerifiedMagnet(t *testing.T) {
	t.Parallel()
	gateway, err := internetarchive.New("https://archive.org/", http.DefaultClient)
	require.NoError(t, err)
	hash := "97542f391e9b4c574e721bf95757fe897b0b43fe"
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "tears-of-steel", Title: "Tears of Steel (2012)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 382260309, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
	})
	require.NoError(t, err)

	material, err := gateway.Materialize(context.Background(), release)

	require.NoError(t, err)
	require.Equal(t, acquisition.TorrentInputMagnet, material.Kind())
	require.Equal(t, hash, material.InfoHash())
}
