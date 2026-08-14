package internetarchive_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/gateway/internetarchive"
	"github.com/stretchr/testify/require"
)

func TestGatewayListsLicensedExactMediaFilesInStableOrder(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/metadata/fixture", request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{
			"metadata":{"licenseurl":"http://creativecommons.org/licenses/by-nc-nd/3.0/"},
			"files":[
				{"name":"zeta.mkv","size":"20","sha1":"2222222222222222222222222222222222222222","format":"Matroska","source":"original"},
				{"name":"alpha.mp4","size":"10","sha1":"1111111111111111111111111111111111111111","format":"h.264","source":"derivative"},
				{"name":"sample.mp4","size":"1","sha1":"3333333333333333333333333333333333333333","format":"h.264","source":"derivative"},
				{"name":"subtitle.srt","size":"5","sha1":"4444444444444444444444444444444444444444","format":"SubRip","source":"original"},
				{"name":"hashless.mp4","size":"5","format":"h.264","source":"original"}
			]
		}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)

	candidates, err := gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))

	require.NoError(t, err)
	require.Len(t, candidates, 3)
	require.Equal(t, "zeta.mkv", candidates[0].Media().Name)
	require.Equal(t, "alpha.mp4", candidates[1].Media().Name)
	require.Equal(t, "sample.mp4", candidates[2].Media().Name)
	for _, candidate := range candidates {
		require.Equal(t, internetarchive.FileProviderName, candidate.Media().Backing().Provider)
		require.NotContains(t, candidate.Media().ObjectID, "http")
		require.NotContains(t, candidate.Media().ObjectID, "fixture")
		require.Equal(t, "Internet Archive", candidate.Indexer())
	}
}

func TestGatewayRejectsMissingOrUnsupportedArchiveLicense(t *testing.T) {
	t.Parallel()
	for _, license := range []string{"", "https://example.test/all-rights-reserved"} {
		license := license
		t.Run(fmt.Sprintf("license-%d", len(license)), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, err := fmt.Fprintf(writer, `{"metadata":{"licenseurl":%q},"files":[]}`, license)
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			gateway, err := internetarchive.New(server.URL+"/", server.Client())
			require.NoError(t, err)

			_, err = gateway.ListRangeCandidates(context.Background(), archiveFixtureRelease(t, server.URL))

			require.ErrorContains(t, err, "license")
			require.NotContains(t, strings.ToLower(err.Error()), "fixture")
		})
	}
}

func TestGatewayRangeCandidateIDsAreDeterministicAndReleaseBound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, err := writer.Write([]byte(`{
			"metadata":{"licenseurl":"https://creativecommons.org/publicdomain/mark/1.0/"},
			"files":[{"name":"movie.mp4","size":16,"sha1":"1111111111111111111111111111111111111111","source":"original"}]
		}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	gateway, err := internetarchive.New(server.URL+"/", server.Client())
	require.NoError(t, err)
	release := archiveFixtureRelease(t, server.URL)

	first, err := gateway.ListRangeCandidates(context.Background(), release)
	require.NoError(t, err)
	second, err := gateway.ListRangeCandidates(context.Background(), release)
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Len(t, first, 1)
	require.LessOrEqual(t, len(first[0].Media().ObjectID), 512)

	other, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "prowlarr", SourceID: "fixture", Title: "Fixture", Protocol: acquisition.ReleaseProtocolTorrent,
		Size: 16, Indexer: "test", InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	_, err = gateway.ListRangeCandidates(context.Background(), other)
	require.Error(t, err)
}

func archiveFixtureRelease(t *testing.T, baseURL string) acquisition.Release {
	t.Helper()
	hash := "0123456789abcdef0123456789abcdef01234567"
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Fixture (2026)",
		Protocol: acquisition.ReleaseProtocolTorrent, Size: 16, Indexer: "Internet Archive",
		InfoHash: hash, MagnetURL: "magnet:?xt=urn:btih:" + hash,
		DownloadURL: baseURL + "/download/fixture/fixture_archive.torrent",
	})
	require.NoError(t, err)
	return release
}
