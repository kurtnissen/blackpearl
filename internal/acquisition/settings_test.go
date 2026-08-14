package acquisition_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/stretchr/testify/require"
)

func TestNewSearchProviderSettingsValidatesAndRedactsCredential(t *testing.T) {
	t.Parallel()
	settings, err := acquisition.NewSearchProviderSettings("prowlarr", "http://prowlarr:9696/base/", "private-api-key")

	require.NoError(t, err)
	require.Equal(t, "prowlarr", settings.Provider())
	require.Equal(t, "http://prowlarr:9696/base/", settings.Endpoint())
	require.Equal(t, "private-api-key", settings.Credential())
	require.NotContains(t, fmt.Sprint(settings), "private-api-key")
	require.NotContains(t, fmt.Sprintf("%#v", settings), "private-api-key")
}

func TestNewSearchProviderSettingsRejectsUnsafeValuesWithoutEcho(t *testing.T) {
	t.Parallel()
	secret := "private-settings-secret"
	tests := []struct {
		name       string
		provider   string
		endpoint   string
		credential string
	}{
		{name: "provider missing", endpoint: "http://prowlarr:9696", credential: secret},
		{name: "provider unsafe", provider: "Prowlarr", endpoint: "http://prowlarr:9696", credential: secret},
		{name: "endpoint relative", provider: "prowlarr", endpoint: "/api", credential: secret},
		{name: "endpoint credentials", provider: "prowlarr", endpoint: "http://user:pass@prowlarr:9696", credential: secret},
		{name: "endpoint query", provider: "prowlarr", endpoint: "http://prowlarr:9696?key=value", credential: secret},
		{name: "endpoint fragment", provider: "prowlarr", endpoint: "http://prowlarr:9696#fragment", credential: secret},
		{name: "endpoint whitespace", provider: "prowlarr", endpoint: " http://prowlarr:9696", credential: secret},
		{name: "credential missing", provider: "prowlarr", endpoint: "http://prowlarr:9696"},
		{name: "credential whitespace", provider: "prowlarr", endpoint: "http://prowlarr:9696", credential: " " + secret},
		{name: "credential control", provider: "prowlarr", endpoint: "http://prowlarr:9696", credential: secret + "\n"},
		{name: "credential too long", provider: "prowlarr", endpoint: "http://prowlarr:9696", credential: strings.Repeat("x", 4097)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := acquisition.NewSearchProviderSettings(test.provider, test.endpoint, test.credential)

			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), "user:pass")
		})
	}
}
