package acquisition

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/kurtnissen/blackpearl/internal/domain"
)

const (
	maximumSearchProviderEndpointBytes   = 2048
	maximumSearchProviderCredentialBytes = 4096
)

// SearchProviderSettings is one immutable, write-only provider connection.
// String formatting intentionally redacts both endpoint and credential.
type SearchProviderSettings struct {
	provider   string
	endpoint   string
	credential string
}

// NewSearchProviderSettings validates one authorized search-provider connection.
func NewSearchProviderSettings(provider string, endpoint string, credential string) (SearchProviderSettings, error) {
	if _, err := domain.NewBackingRef(provider, "settings"); err != nil {
		return SearchProviderSettings{}, errors.New("search provider name is invalid")
	}
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || len(endpoint) > maximumSearchProviderEndpointBytes || strings.IndexFunc(endpoint, unicode.IsControl) >= 0 {
		return SearchProviderSettings{}, errors.New("search provider endpoint is invalid")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return SearchProviderSettings{}, errors.New("search provider endpoint must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if credential == "" || strings.TrimSpace(credential) != credential || len(credential) > maximumSearchProviderCredentialBytes || strings.IndexFunc(credential, unicode.IsControl) >= 0 {
		return SearchProviderSettings{}, errors.New("search provider credential is invalid")
	}
	return SearchProviderSettings{provider: provider, endpoint: parsed.String(), credential: credential}, nil
}

// Provider returns the configured search adapter name.
func (s SearchProviderSettings) Provider() string { return s.provider }

// Endpoint returns the configured provider endpoint for gateway construction.
func (s SearchProviderSettings) Endpoint() string { return s.endpoint }

// Credential returns the write-only provider credential for gateway construction.
func (s SearchProviderSettings) Credential() string { return s.credential }

// String redacts connection details from ordinary formatted output.
func (s SearchProviderSettings) String() string {
	return fmt.Sprintf("SearchProviderSettings{provider:%s endpoint:[redacted] credential:[redacted]}", s.provider)
}

// GoString redacts connection details from Go-syntax formatted output.
func (s SearchProviderSettings) GoString() string { return s.String() }
