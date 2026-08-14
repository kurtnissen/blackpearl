package torbox

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
	"sync"
	"time"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	providerName         = "torbox-torrent"
	maximumResponseBody  = 1 << 20
	sharedRequestTimeout = 30 * time.Second
)

// Options configures read-only access to completed TorBox torrent files.
type Options struct {
	APIBaseURL  string
	APIToken    string
	MetadataTTL time.Duration
	LinkTTL     time.Duration
}

// Gateway maps TorBox torrent files into provider-neutral range sources.
type Gateway struct {
	baseURL     *url.URL
	token       string
	client      *http.Client
	metadataTTL time.Duration
	linkTTL     time.Duration
	now         func() time.Time

	mu       sync.Mutex
	metadata map[objectID]cachedMetadata
	links    map[string]cachedLink
	inflight map[string]*linkCall
}

type fileMetadata struct {
	size      int64
	validator string
}

type cachedMetadata struct {
	metadata fileMetadata
	expires  time.Time
}

type cachedLink struct {
	url     *url.URL
	expires time.Time
}

type linkCall struct {
	done chan struct{}
	url  *url.URL
	err  error
}

type cdnStatusError struct {
	status int
}

func (e *cdnStatusError) Error() string {
	return fmt.Sprintf("TorBox CDN validation requires status 206: got %d", e.status)
}

type apiEnvelope[T any] struct {
	Success bool   `json:"success"`
	Detail  string `json:"detail"`
	Data    T      `json:"data"`
}

type torrentRecord struct {
	ID               int64        `json:"id"`
	Hash             string       `json:"hash"`
	DownloadFinished bool         `json:"download_finished"`
	DownloadPresent  bool         `json:"download_present"`
	Files            []fileRecord `json:"files"`
}

type fileRecord struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
	MD5      string `json:"md5"`
	Zipped   bool   `json:"zipped"`
	Infected bool   `json:"infected"`
}

// New constructs a TorBox gateway without performing network I/O.
func New(options Options, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(options.APIBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("TorBox API base must be an absolute HTTP URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(options.APIToken) == "" || strings.TrimSpace(options.APIToken) != options.APIToken {
		return nil, errors.New("TorBox API token is required without surrounding whitespace")
	}
	if options.MetadataTTL <= 0 || options.LinkTTL <= 0 {
		return nil, errors.New("TorBox metadata and link TTLs must be positive")
	}
	if client == nil {
		return nil, errors.New("TorBox HTTP client is required")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{
		baseURL: parsed, token: options.APIToken, client: &isolatedClient,
		metadataTTL: options.MetadataTTL, linkTTL: options.LinkTTL, now: time.Now,
		metadata: make(map[objectID]cachedMetadata), links: make(map[string]cachedLink), inflight: make(map[string]*linkCall),
	}, nil
}

// Ready verifies local TorBox configuration.
func (g *Gateway) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check TorBox readiness: %w", err)
	}
	return nil
}

// Open resolves immutable metadata and a short-lived CDN link.
func (g *Gateway) Open(ctx context.Context, backing domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open TorBox source: %w", err)
	}
	if backing.Provider != providerName {
		return nil, fmt.Errorf("unsupported TorBox provider: %q", backing.Provider)
	}
	identifier, err := parseObjectID(backing.ObjectID)
	if err != nil {
		return nil, err
	}
	metadata, err := g.loadMetadata(ctx, identifier)
	if err != nil {
		return nil, err
	}
	downloadURL, cached, err := g.downloadURL(ctx, identifier, metadata.validator, false)
	if err != nil {
		return nil, err
	}
	if !cached {
		if err := g.validateDownload(ctx, downloadURL, metadata.size); err != nil {
			if !expiredDownloadError(err) {
				return nil, err
			}
			g.invalidateDownloadURL(identifier, metadata.validator, downloadURL)
			downloadURL, _, err = g.downloadURL(ctx, identifier, metadata.validator, true)
			if err != nil {
				return nil, err
			}
			if err := g.validateDownload(ctx, downloadURL, metadata.size); err != nil {
				return nil, err
			}
		}
		g.cacheDownloadURL(identifier, metadata.validator, downloadURL)
	}
	return newSource(g, identifier, metadata, downloadURL), nil
}

func (g *Gateway) validateDownload(ctx context.Context, downloadURL *url.URL, expectedSize int64) (resultErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return errors.New("construct TorBox CDN validation request")
	}
	request.Header.Set("Range", "bytes=0-0")
	response, err := g.client.Do(request)
	if err != nil {
		return errors.New("request TorBox CDN validation range")
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusPartialContent {
		return &cdnStatusError{status: response.StatusCode}
	}
	if err := validateContentRange(response.Header.Get("Content-Range"), 0, 0, expectedSize); err != nil {
		return fmt.Errorf("TorBox CDN size mismatch or invalid validation range: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2))
	if err != nil {
		return fmt.Errorf("read TorBox CDN validation range: %w", err)
	}
	if len(body) != 1 {
		return fmt.Errorf("TorBox CDN validation body length mismatch: got %d want 1", len(body))
	}
	return nil
}

func (g *Gateway) loadMetadata(ctx context.Context, identifier objectID) (fileMetadata, error) {
	g.mu.Lock()
	if cached, ok := g.metadata[identifier]; ok && g.now().Before(cached.expires) {
		g.mu.Unlock()
		return cached.metadata, nil
	}
	g.mu.Unlock()
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/mylist")
	if err != nil {
		return fileMetadata{}, fmt.Errorf("construct TorBox metadata URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fileMetadata{}, fmt.Errorf("construct TorBox metadata request: %w", err)
	}
	query := request.URL.Query()
	query.Set("id", strconv.FormatInt(identifier.TorrentID, 10))
	query.Set("bypass_cache", "true")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSON(request, &envelope); err != nil {
		return fileMetadata{}, fmt.Errorf("request TorBox metadata: %w", err)
	}
	if !envelope.Success {
		return fileMetadata{}, fmt.Errorf("TorBox metadata rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var torrent torrentRecord
	if err := json.Unmarshal(envelope.Data, &torrent); err != nil {
		var torrents []torrentRecord
		if listErr := json.Unmarshal(envelope.Data, &torrents); listErr != nil || len(torrents) != 1 {
			return fileMetadata{}, errors.New("decode TorBox torrent metadata")
		}
		torrent = torrents[0]
	}
	if torrent.ID != identifier.TorrentID {
		return fileMetadata{}, fmt.Errorf("TorBox torrent %d not found", identifier.TorrentID)
	}
	if !torrent.DownloadFinished {
		return fileMetadata{}, fmt.Errorf("TorBox torrent %d is not complete", identifier.TorrentID)
	}
	if !torrent.DownloadPresent {
		return fileMetadata{}, fmt.Errorf("TorBox torrent %d download is not present", identifier.TorrentID)
	}
	var selected *fileRecord
	for index := range torrent.Files {
		if torrent.Files[index].ID == identifier.FileID {
			selected = &torrent.Files[index]
			break
		}
	}
	if selected == nil {
		return fileMetadata{}, fmt.Errorf("TorBox file %d not found", identifier.FileID)
	}
	if selected.Zipped {
		return fileMetadata{}, fmt.Errorf("TorBox file %d is zipped", identifier.FileID)
	}
	if selected.Infected {
		return fileMetadata{}, fmt.Errorf("TorBox file %d is infected", identifier.FileID)
	}
	if selected.Size <= 0 {
		return fileMetadata{}, fmt.Errorf("TorBox file %d requires positive size", identifier.FileID)
	}
	hashType, hashValue := "hash", strings.TrimSpace(selected.Hash)
	if hashValue == "" {
		hashType, hashValue = "md5", strings.TrimSpace(selected.MD5)
	}
	if hashValue == "" {
		return fileMetadata{}, fmt.Errorf("TorBox file %d requires a stable hash", identifier.FileID)
	}
	metadata := fileMetadata{size: selected.Size, validator: fmt.Sprintf("torbox:%s:%s:%d", hashType, hashValue, selected.Size)}
	g.mu.Lock()
	g.metadata[identifier] = cachedMetadata{metadata: metadata, expires: g.now().Add(g.metadataTTL)}
	g.mu.Unlock()
	return metadata, nil
}

func (g *Gateway) downloadURL(ctx context.Context, identifier objectID, validator string, force bool) (*url.URL, bool, error) {
	key := identifier.String() + "\x00" + validator
	g.mu.Lock()
	if cached, ok := g.links[key]; !force && ok && g.now().Before(cached.expires) {
		g.mu.Unlock()
		return cached.url, true, nil
	}
	if call, ok := g.inflight[key]; ok {
		done := call.done
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-done:
			return call.url, false, call.err
		}
	}
	call := &linkCall{done: make(chan struct{})}
	g.inflight[key] = call
	g.mu.Unlock()
	go g.resolveDownloadURL(call, key, identifier)
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-call.done:
		return call.url, false, call.err
	}
}

func (g *Gateway) resolveDownloadURL(call *linkCall, key string, identifier objectID) {
	requestContext, cancel := context.WithTimeout(context.Background(), sharedRequestTimeout)
	defer cancel()
	downloadURL, downloadErr := g.requestDownloadURL(requestContext, identifier)
	g.mu.Lock()
	call.url = downloadURL
	call.err = downloadErr
	delete(g.inflight, key)
	close(call.done)
	g.mu.Unlock()
}

func (g *Gateway) cacheDownloadURL(identifier objectID, validator string, downloadURL *url.URL) {
	key := identifier.String() + "\x00" + validator
	g.mu.Lock()
	g.links[key] = cachedLink{url: downloadURL, expires: g.now().Add(g.linkTTL)}
	g.mu.Unlock()
}

func (g *Gateway) invalidateDownloadURL(identifier objectID, validator string, downloadURL *url.URL) {
	key := identifier.String() + "\x00" + validator
	g.mu.Lock()
	if cached, ok := g.links[key]; ok && cached.url.String() == downloadURL.String() {
		delete(g.links, key)
	}
	g.mu.Unlock()
}

func expiredDownloadError(err error) bool {
	var statusError *cdnStatusError
	if !errors.As(err, &statusError) {
		return false
	}
	return statusError.status == http.StatusUnauthorized || statusError.status == http.StatusForbidden || statusError.status == http.StatusGone
}

func (g *Gateway) requestDownloadURL(ctx context.Context, identifier objectID) (*url.URL, error) {
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/requestdl")
	if err != nil {
		return nil, fmt.Errorf("construct TorBox download request URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("construct TorBox download request: %w", err)
	}
	query := request.URL.Query()
	query.Set("token", g.token)
	query.Set("torrent_id", strconv.FormatInt(identifier.TorrentID, 10))
	query.Set("file_id", strconv.FormatInt(identifier.FileID, 10))
	query.Set("redirect", "false")
	query.Set("append_name", "false")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[string]
	if err := g.doJSON(request, &envelope); err != nil {
		return nil, fmt.Errorf("request TorBox download link: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("TorBox download link rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	parsed, err := url.Parse(envelope.Data)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("TorBox returned an invalid HTTPS download URL")
	}
	return parsed, nil
}

func (g *Gateway) doJSON(request *http.Request, destination any) (resultErr error) {
	return g.doJSONLimited(request, destination, maximumResponseBody)
}

func (g *Gateway) doJSONLimited(request *http.Request, destination any, maximumBody int64) (resultErr error) {
	response, err := g.client.Do(request)
	if err != nil {
		if contextErr := request.Context().Err(); contextErr != nil {
			return fmt.Errorf("perform TorBox API request: %w", contextErr)
		}
		return errors.New("perform TorBox API request")
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("TorBox API credentials rejected: %w", domain.ErrUnauthorized)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("TorBox API requires status 200: got %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumBody+1))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode TorBox API response: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("TorBox API response exceeds one JSON value")
	}
	return nil
}

func (g *Gateway) sanitizeDetail(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			continue
		}
		builder.WriteRune(character)
		if builder.Len() >= 256 {
			break
		}
	}
	result := strings.ReplaceAll(builder.String(), g.token, "[redacted]")
	words := strings.Fields(result)
	for index, word := range words {
		if strings.Contains(word, "https://") || strings.Contains(word, "http://") {
			words[index] = "[redacted-url]"
		}
	}
	result = strings.Join(words, " ")
	if strings.TrimSpace(result) == "" {
		return "request failed"
	}
	return result
}

var _ interface {
	Open(context.Context, domain.BackingRef) (acquisition.RangeSource, error)
	Ready(context.Context) error
} = (*Gateway)(nil)
