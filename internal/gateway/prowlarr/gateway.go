// Package prowlarr maps authorized Prowlarr search results into provider-neutral releases.
package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	providerName            = "prowlarr"
	maximumAPIKeyBytes      = 4096
	maximumSearchBodyBytes  = 8 << 20
	maximumSearchResultRows = 100
)

// Options configures read-only Prowlarr search access.
type Options struct {
	BaseURL string
	APIKey  string
}

// Gateway searches explicitly configured Prowlarr indexers.
type Gateway struct {
	baseURL *url.URL
	apiKey  string
	client  *http.Client
}

type releaseResource struct {
	ID          int64  `json:"id"`
	GUID        string `json:"guid"`
	Size        int64  `json:"size"`
	IndexerID   int64  `json:"indexerId"`
	Indexer     string `json:"indexer"`
	Title       string `json:"title"`
	Protocol    string `json:"protocol"`
	ReleaseHash string `json:"releaseHash"`
	InfoHash    string `json:"infoHash"`
	MagnetURL   string `json:"magnetUrl"`
	DownloadURL string `json:"downloadUrl"`
	Seeders     *int   `json:"seeders"`
}

// New constructs a Prowlarr search gateway without network I/O.
func New(options Options, client *http.Client) (*Gateway, error) {
	if client == nil {
		return nil, errors.New("prowlarr HTTP client is required")
	}
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("prowlarr base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if options.APIKey == "" || strings.TrimSpace(options.APIKey) != options.APIKey || len(options.APIKey) > maximumAPIKeyBytes || strings.IndexFunc(options.APIKey, unicode.IsControl) >= 0 {
		return nil, errors.New("prowlarr API key is required without surrounding whitespace and must not exceed 4096 bytes")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{baseURL: parsed, apiKey: options.APIKey, client: &isolatedClient}, nil
}

// Name returns the stable provider name used by the resolver.
func (g *Gateway) Name() string { return providerName }

// Capabilities describes the release locators Prowlarr can normalize.
func (g *Gateway) Capabilities() acquisition.ProviderCapabilities {
	return acquisition.NewProviderCapabilities(
		[]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent, acquisition.ReleaseProtocolUsenet},
		true,
		true,
		true,
	)
}

// Search returns valid normalized releases from configured indexers.
func (g *Gateway) Search(ctx context.Context, search acquisition.SearchRequest) (_ []acquisition.Release, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search Prowlarr: %w", err)
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "api/v1/search")
	if err != nil {
		return nil, errors.New("construct Prowlarr search URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("construct Prowlarr search request")
	}
	query := request.URL.Query()
	query.Set("query", search.Query())
	query.Set("type", "search")
	query.Set("limit", strconv.Itoa(maximumSearchResultRows))
	request.URL.RawQuery = query.Encode()
	request.Header.Set("X-Api-Key", g.apiKey)
	response, err := g.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("request Prowlarr search: %w", ctxErr)
		}
		return nil, errors.New("request Prowlarr search")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Prowlarr search response"))
		}
	}()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("prowlarr rejected API credentials: %w", domain.ErrUnauthorized)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr search returned HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumSearchBodyBytes+1))
	if err != nil {
		return nil, errors.New("read Prowlarr search response")
	}
	if len(body) > maximumSearchBodyBytes {
		return nil, errors.New("prowlarr search response exceeds 8 MiB")
	}
	var resources []releaseResource
	if err := json.Unmarshal(body, &resources); err != nil {
		return nil, errors.New("decode Prowlarr search response")
	}
	releases := make([]acquisition.Release, 0, len(resources))
	for index := range resources {
		release, valid := normalizeRelease(resources[index])
		if valid {
			releases = append(releases, release)
		}
	}
	return releases, nil
}

func normalizeRelease(resource releaseResource) (acquisition.Release, bool) {
	protocol := acquisition.ReleaseProtocol(strings.ToLower(strings.TrimSpace(resource.Protocol)))
	if protocol != acquisition.ReleaseProtocolTorrent && protocol != acquisition.ReleaseProtocolUsenet {
		return acquisition.Release{}, false
	}
	sourceID := strings.TrimSpace(resource.GUID)
	if sourceID == "" && resource.IndexerID > 0 && resource.ID > 0 {
		sourceID = strconv.FormatInt(resource.IndexerID, 10) + ":" + strconv.FormatInt(resource.ID, 10)
	}
	indexer := strings.TrimSpace(resource.Indexer)
	if indexer == "" && resource.IndexerID > 0 {
		indexer = "indexer-" + strconv.FormatInt(resource.IndexerID, 10)
	}
	infoHash := strings.TrimSpace(resource.InfoHash)
	if infoHash == "" {
		infoHash = strings.TrimSpace(resource.ReleaseHash)
	}
	release, err := acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: providerName, SourceID: sourceID, Title: resource.Title, Protocol: protocol, Size: resource.Size,
		Indexer: indexer, InfoHash: infoHash, MagnetURL: resource.MagnetURL,
		DownloadURL: resource.DownloadURL, Seeders: resource.Seeders,
	})
	return release, err == nil
}
