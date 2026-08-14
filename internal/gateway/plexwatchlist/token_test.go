package plexwatchlist_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/gateway/plexwatchlist"
	"github.com/stretchr/testify/require"
)

func TestPreferencesTokenSourceReadsCurrentTokenWithoutRetainingOldValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "Preferences.xml")
	require.NoError(t, os.WriteFile(path, []byte(`<Preferences MachineIdentifier="machine" PlexOnlineToken="first-token"/>`), 0o600))
	source, err := plexwatchlist.NewPreferencesTokenSource(path)
	require.NoError(t, err)

	first, err := source.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "first-token", first)
	require.NoError(t, os.WriteFile(path, []byte(`<Preferences PlexOnlineToken="replacement-token"/>`), 0o600))
	second, err := source.Token(context.Background())

	require.NoError(t, err)
	require.Equal(t, "replacement-token", second)
}

func TestTokenFileSourceReadsTrimmedPrivateValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plex-token")
	require.NoError(t, os.WriteFile(path, []byte("private-token\n"), 0o600))
	source, err := plexwatchlist.NewTokenFileSource(path)
	require.NoError(t, err)

	token, err := source.Token(context.Background())

	require.NoError(t, err)
	require.Equal(t, "private-token", token)
}

func TestTokenSourcesRejectUnsafeOrUnavailableInputWithoutLeaks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	privatePath := filepath.Join(root, "private-preferences.xml")
	secret := "never-return-this-token"
	tests := []struct {
		name      string
		construct func(string) (plexwatchlist.TokenSource, error)
		content   string
		cancel    bool
	}{
		{name: "relative preferences path", construct: plexwatchlist.NewPreferencesTokenSource},
		{name: "relative token path", construct: plexwatchlist.NewTokenFileSource},
		{name: "malformed preferences", construct: plexwatchlist.NewPreferencesTokenSource, content: `<Preferences PlexOnlineToken="` + secret + `">`},
		{name: "missing preferences token", construct: plexwatchlist.NewPreferencesTokenSource, content: `<Preferences MachineIdentifier="machine"/>`},
		{name: "oversize preferences", construct: plexwatchlist.NewPreferencesTokenSource, content: strings.Repeat("x", 1_048_577)},
		{name: "control in token file", construct: plexwatchlist.NewTokenFileSource, content: "bad\x00token"},
		{name: "oversize token file", construct: plexwatchlist.NewTokenFileSource, content: strings.Repeat("x", 4097)},
		{name: "cancelled read", construct: plexwatchlist.NewPreferencesTokenSource, content: `<Preferences PlexOnlineToken="` + secret + `"/>`, cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := "relative.xml"
			if test.content != "" {
				path = privatePath + strings.ReplaceAll(test.name, " ", "-")
				require.NoError(t, os.WriteFile(path, []byte(test.content), 0o600))
			}
			source, err := test.construct(path)
			if err == nil {
				ctx := context.Background()
				if test.cancel {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
				_, err = source.Token(ctx)
			}
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), root)
		})
	}
}
