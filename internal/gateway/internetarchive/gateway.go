// Package internetarchive searches the public Internet Archive metadata API
// for openly distributed BitTorrent items.
package internetarchive

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

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
)

const (
	providerName       = "internet-archive"
	maximumResultRows  = 100
	maximumBodyBytes   = 2 << 20
	internetArchiveTag = "Internet Archive"
)

// Gateway provides read-only open-media search and transient magnet material.
type Gateway struct {
	baseURL *url.URL
	client  *http.Client
}

type searchEnvelope struct {
	Response struct {
		Docs []searchDocument `json:"docs"`
	} `json:"response"`
}

type searchDocument struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	ItemSize   int64  `json:"item_size"`
	InfoHash   string `json:"btih"`
}

// New constructs an Internet Archive search gateway without network I/O.
func New(baseURL string, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Internet Archive base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if client == nil {
		return nil, errors.New("Internet Archive HTTP client is required")
	}
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{baseURL: parsed, client: &isolated}, nil
}

// Name returns the stable provider name used in release fingerprints.
func (g *Gateway) Name() string { return providerName }

// Capabilities reports verified torrent info hashes and magnet material.
func (g *Gateway) Capabilities() acquisition.ProviderCapabilities {
	return acquisition.NewProviderCapabilities([]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent}, true, true, false)
}

// Search returns normalized public Archive BitTorrent items for one intent.
func (g *Gateway) Search(ctx context.Context, search acquisition.SearchRequest) (_ []acquisition.Release, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search Internet Archive: %w", err)
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "advancedsearch.php")
	if err != nil {
		return nil, errors.New("construct Internet Archive search URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("construct Internet Archive search request")
	}
	query := request.URL.Query()
	query.Set("q", archiveQuery(search))
	query.Add("fl[]", "identifier")
	query.Add("fl[]", "title")
	query.Add("fl[]", "item_size")
	query.Add("fl[]", "btih")
	query.Set("sort", "-downloads")
	query.Set("rows", strconv.Itoa(maximumResultRows))
	query.Set("output", "json")
	request.URL.RawQuery = query.Encode()
	response, err := g.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("request Internet Archive search: %w", ctxErr)
		}
		return nil, errors.New("request Internet Archive search")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Internet Archive search response"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Internet Archive search returned HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBodyBytes+1))
	if err != nil {
		return nil, errors.New("read Internet Archive search response")
	}
	if len(body) > maximumBodyBytes {
		return nil, errors.New("Internet Archive search response exceeds 2 MiB")
	}
	var envelope searchEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("decode Internet Archive search response")
	}
	releases := make([]acquisition.Release, 0, len(envelope.Response.Docs))
	for _, document := range envelope.Response.Docs {
		release, releaseErr := archiveRelease(document)
		if releaseErr == nil {
			releases = append(releases, release)
		}
	}
	return releases, nil
}

// Materialize returns the already verified magnet without persisting it.
func (g *Gateway) Materialize(ctx context.Context, release acquisition.Release) (acquisition.TorrentInput, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.TorrentInput{}, fmt.Errorf("materialize Internet Archive release: %w", err)
	}
	if release.Provider() != providerName || release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" || release.MagnetURL() == "" {
		return acquisition.TorrentInput{}, errors.New("Internet Archive materialization requires its validated torrent magnet")
	}
	input, err := acquisition.NewMagnetTorrentInput(release.InfoHash(), release.MagnetURL())
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("validate Internet Archive magnet material")
	}
	return input, nil
}

func archiveQuery(search acquisition.SearchRequest) string {
	title := quoteSearch(search.Title())
	if search.Episode() > 0 {
		episode := fmt.Sprintf("S%02dE%02d", search.Season(), search.Episode())
		return fmt.Sprintf(`title:%s AND title:%q AND format:"Archive BitTorrent"`, title, episode)
	}
	return fmt.Sprintf(`title:%s AND title:%d AND format:"Archive BitTorrent"`, title, search.Year())
}

func quoteSearch(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + escaped + `"`
}

func archiveRelease(document searchDocument) (acquisition.Release, error) {
	values := url.Values{}
	values.Set("xt", "urn:btih:"+document.InfoHash)
	values.Set("dn", document.Identifier)
	return acquisition.NewRelease(acquisition.ReleaseInput{
		Provider: providerName, SourceID: document.Identifier, Title: document.Title,
		Protocol: acquisition.ReleaseProtocolTorrent, Size: document.ItemSize,
		Indexer: internetArchiveTag, InfoHash: document.InfoHash,
		MagnetURL: "magnet:?" + values.Encode(),
	})
}
