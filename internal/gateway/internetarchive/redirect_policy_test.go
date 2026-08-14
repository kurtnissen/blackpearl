package internetarchive

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArchiveRedirectPolicyAllowsOnlyBoundedOfficialDestinations(t *testing.T) {
	t.Parallel()
	base, err := url.Parse("https://archive.org/")
	require.NoError(t, err)
	policy := archiveRedirectPolicy(base)
	via := []*http.Request{{URL: base}}

	offload, err := url.Parse("https://ia800706.us.archive.org/items/fixture.torrent")
	require.NoError(t, err)
	require.NoError(t, policy(&http.Request{URL: offload}, via))

	sameOriginURL, err := url.Parse("https://archive.org/download/fixture.torrent")
	require.NoError(t, err)
	require.NoError(t, policy(&http.Request{URL: sameOriginURL}, via))

	outside, err := url.Parse("https://evilarchive.org/fixture.torrent")
	require.NoError(t, err)
	require.Error(t, policy(&http.Request{URL: outside}, via))

	credentialed, err := url.Parse("https://user@archive.org/fixture.torrent")
	require.NoError(t, err)
	require.Error(t, policy(&http.Request{URL: credentialed}, via))

	require.Error(t, policy(&http.Request{URL: sameOriginURL}, []*http.Request{{}, {}, {}}))
}
