// Package internetarchive searches the public Internet Archive metadata API
// for openly distributed BitTorrent items.
package internetarchive

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	baseURL        *url.URL
	client         *http.Client
	downloadClient *http.Client
}

type searchEnvelope struct {
	Response struct {
		Docs []searchDocument `json:"docs"`
	} `json:"response"`
}

type searchDocument struct {
	Identifier string          `json:"identifier"`
	Title      string          `json:"title"`
	Year       json.RawMessage `json:"year"`
	ItemSize   int64           `json:"item_size"`
	InfoHash   string          `json:"btih"`
}

// New constructs an Internet Archive search gateway without network I/O.
func New(baseURL string, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("internet Archive base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if client == nil {
		return nil, errors.New("internet Archive HTTP client is required")
	}
	isolated := *client
	isolated.CheckRedirect = archiveRedirectPolicy(parsed)
	download := isolated
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		transport = http.DefaultTransport.(*http.Transport)
	}
	clone := transport.Clone()
	clone.ForceAttemptHTTP2 = false
	if clone.TLSHandshakeTimeout < 30*time.Second {
		clone.TLSHandshakeTimeout = 30 * time.Second
	}
	clone.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.NextProtos = []string{"http/1.1"}
	download.Transport = clone
	return &Gateway{baseURL: parsed, client: &isolated, downloadClient: &download}, nil
}

// Ready validates local gateway state without downloading media content.
func (g *Gateway) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check Internet Archive readiness: %w", err)
	}
	return nil
}

// Name returns the stable provider name used in release fingerprints.
func (g *Gateway) Name() string { return providerName }

// Capabilities reports verified torrent info hashes and magnet material.
func (g *Gateway) Capabilities() acquisition.ProviderCapabilities {
	return acquisition.NewProviderCapabilities([]acquisition.ReleaseProtocol{acquisition.ReleaseProtocolTorrent}, true, true, true)
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
	query.Add("fl[]", "year")
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
		return nil, fmt.Errorf("internet Archive search returned HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBodyBytes+1))
	if err != nil {
		return nil, errors.New("read Internet Archive search response")
	}
	if len(body) > maximumBodyBytes {
		return nil, errors.New("internet Archive search response exceeds 2 MiB")
	}
	var envelope searchEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("decode Internet Archive search response")
	}
	releases := make([]acquisition.Release, 0, len(envelope.Response.Docs))
	for _, document := range envelope.Response.Docs {
		release, releaseErr := archiveRelease(g.baseURL, document, search)
		if releaseErr == nil {
			releases = append(releases, release)
		}
	}
	return releases, nil
}

// Materialize downloads the provider-owned torrent metadata, verifies its
// info hash, and returns the bounded bytes without persisting them.
func (g *Gateway) Materialize(ctx context.Context, release acquisition.Release) (_ acquisition.TorrentInput, resultErr error) {
	if err := ctx.Err(); err != nil {
		return acquisition.TorrentInput{}, fmt.Errorf("materialize Internet Archive release: %w", err)
	}
	if release.Provider() != providerName || release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" || release.DownloadURL() == "" {
		return acquisition.TorrentInput{}, errors.New("internet Archive materialization requires its validated torrent file")
	}
	expectedURL, err := archiveTorrentURL(g.baseURL, release.SourceID())
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("construct Internet Archive torrent material URL")
	}
	if release.DownloadURL() != expectedURL {
		return acquisition.TorrentInput{}, errors.New("internet Archive material URL is outside the selected item")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, expectedURL, nil)
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("construct Internet Archive torrent material request")
	}
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream")
	response, err := g.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return acquisition.TorrentInput{}, fmt.Errorf("request Internet Archive torrent material: %w", ctxErr)
		}
		return acquisition.TorrentInput{}, errors.New("request Internet Archive torrent material")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Internet Archive torrent material response"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return acquisition.TorrentInput{}, fmt.Errorf("internet Archive torrent material returned HTTP status %d", response.StatusCode)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType == "text/html" || mediaType == "application/json" || strings.HasPrefix(mediaType, "text/") {
		return acquisition.TorrentInput{}, errors.New("internet Archive material response is not a torrent file")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, acquisition.MaximumTorrentFileBytes+1))
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("read Internet Archive torrent material")
	}
	if len(payload) > acquisition.MaximumTorrentFileBytes {
		return acquisition.TorrentInput{}, fmt.Errorf("internet Archive torrent material exceeds %d bytes", acquisition.MaximumTorrentFileBytes)
	}
	input, err := acquisition.NewTorrentFileInput(release.InfoHash(), payload)
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("validate Internet Archive torrent material")
	}
	return input, nil
}

func archiveQuery(search acquisition.SearchRequest) string {
	title := quoteSearch(search.Title())
	if search.Episode() > 0 {
		episode := fmt.Sprintf("S%02dE%02d", search.Season(), search.Episode())
		return fmt.Sprintf(`title:%s AND title:%q AND format:"Archive BitTorrent"`, title, episode)
	}
	return fmt.Sprintf(`title:%s AND year:%d AND format:"Archive BitTorrent"`, title, search.Year())
}

func quoteSearch(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + escaped + `"`
}

func archiveRelease(baseURL *url.URL, document searchDocument, search acquisition.SearchRequest) (acquisition.Release, error) {
	title := strings.TrimSpace(document.Title)
	if search.Episode() == 0 {
		year, err := archiveDocumentYear(document.Year)
		if err != nil || year != search.Year() {
			return acquisition.Release{}, errors.New("internet Archive result year does not match the request")
		}
		yearText := strconv.Itoa(year)
		if !strings.Contains(title, yearText) {
			title += " (" + yearText + ")"
		}
	}
	values := url.Values{}
	values.Set("xt", "urn:btih:"+document.InfoHash)
	values.Set("dn", document.Identifier)
	input := acquisition.ReleaseInput{
		Provider: providerName, SourceID: document.Identifier, Title: title,
		Protocol: acquisition.ReleaseProtocolTorrent, Size: document.ItemSize,
		Indexer: internetArchiveTag, InfoHash: document.InfoHash,
		MagnetURL: "magnet:?" + values.Encode(),
	}
	validated, err := acquisition.NewRelease(input)
	if err != nil {
		return acquisition.Release{}, err
	}
	downloadURL, err := archiveTorrentURL(baseURL, validated.SourceID())
	if err != nil {
		return acquisition.Release{}, err
	}
	input.DownloadURL = downloadURL
	return acquisition.NewRelease(input)
}

func archiveDocumentYear(raw json.RawMessage) (int, error) {
	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil && numeric > 0 {
		return numeric, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, errors.New("invalid Internet Archive result year")
	}
	numeric, err := strconv.Atoi(text)
	if err != nil || numeric <= 0 || strconv.Itoa(numeric) != text {
		return 0, errors.New("invalid Internet Archive result year")
	}
	return numeric, nil
}

func archiveTorrentURL(baseURL *url.URL, identifier string) (string, error) {
	if baseURL == nil || identifier == "" || identifier == "." || identifier == ".." || strings.ContainsAny(identifier, `/\\?#`) {
		return "", errors.New("invalid Internet Archive identifier")
	}
	return url.JoinPath(baseURL.String(), "download", identifier, identifier+"_archive.torrent")
}

func archiveRedirectPolicy(baseURL *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("internet Archive material redirect limit exceeded")
		}
		candidate := request.URL
		if candidate == nil || candidate.User != nil || candidate.Fragment != "" {
			return errors.New("internet Archive material redirect is invalid")
		}
		if sameOrigin(baseURL, candidate) {
			return nil
		}
		baseHost := strings.ToLower(baseURL.Hostname())
		candidateHost := strings.ToLower(candidate.Hostname())
		if baseURL.Scheme == "https" && baseHost == "archive.org" && candidate.Scheme == "https" &&
			(candidateHost == "archive.org" || strings.HasSuffix(candidateHost, ".archive.org")) && effectivePort(candidate) == "443" {
			return nil
		}
		return errors.New("internet Archive material redirect left the trusted origin")
	}
}

func sameOrigin(left *url.URL, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) && effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}
