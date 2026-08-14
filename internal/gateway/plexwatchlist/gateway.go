package plexwatchlist

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

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const (
	defaultPageSize     = 50
	defaultMaximumItems = 500
	maximumPageSize     = 100
	maximumItemsLimit   = 1000
	maximumResponseSize = 2 << 20
)

// ErrUnavailable indicates that a watchlist snapshot could not be read safely.
var ErrUnavailable = errors.New("plex watchlist unavailable")

// Options configures bounded Plex watchlist retrieval.
type Options struct {
	BaseURL      string
	PageSize     int
	MaximumItems int
}

// Gateway reads immutable Plex watchlist snapshots.
type Gateway struct {
	baseURL      *url.URL
	pageSize     int
	maximumItems int
	tokens       TokenSource
	client       *http.Client
}

type wireEnvelope struct {
	MediaContainer wireContainer `json:"MediaContainer"`
}

type wireContainer struct {
	Size      int        `json:"size"`
	TotalSize int        `json:"totalSize"`
	Metadata  []wireItem `json:"Metadata"`
}

type wireItem struct {
	GUID  string `json:"guid"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Year  int    `json:"year"`
}

// New validates and constructs a Plex watchlist gateway.
func New(options Options, tokens TokenSource, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("plex watchlist base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if tokens == nil || client == nil {
		return nil, errors.New("plex watchlist token source and HTTP client are required")
	}
	pageSize := options.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if pageSize < 1 || pageSize > maximumPageSize {
		return nil, fmt.Errorf("plex watchlist page size must be between 1 and %d", maximumPageSize)
	}
	maximumItems := options.MaximumItems
	if maximumItems == 0 {
		maximumItems = defaultMaximumItems
	}
	if maximumItems < 1 || maximumItems > maximumItemsLimit {
		return nil, fmt.Errorf("plex watchlist maximum items must be between 1 and %d", maximumItemsLimit)
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("plex watchlist redirects are disabled")
	}
	return &Gateway{baseURL: parsed, pageSize: pageSize, maximumItems: maximumItems, tokens: tokens, client: &boundedClient}, nil
}

// Snapshot returns a bounded, deduplicated watchlist snapshot.
func (g *Gateway) Snapshot(ctx context.Context) ([]acquisition.WatchlistItem, error) {
	ctx, span := otel.Tracer("blackpearl/plexwatchlist").Start(ctx, "plex_watchlist.snapshot")
	defer span.End()
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return nil, sanitizedContextError(ctx, "load Plex watchlist credential")
	}
	token, err = validateToken(token)
	if err != nil {
		return nil, fmt.Errorf("load Plex watchlist credential: %w", ErrUnavailable)
	}
	items := make([]acquisition.WatchlistItem, 0)
	seen := make(map[string]struct{})
	for start := 0; start < g.maximumItems; start += g.pageSize {
		page, total, pageErr := g.fetchPage(ctx, token, start)
		if pageErr != nil {
			return nil, pageErr
		}
		for _, wire := range page {
			item, itemErr := acquisition.NewWatchlistItem(acquisition.WatchlistItemInput{
				Source: "plex-watchlist", ExternalID: wire.GUID, MediaType: acquisition.WatchlistMediaType(wire.Type),
				Title: wire.Title, Year: wire.Year,
			})
			if itemErr != nil {
				continue
			}
			key := item.Source() + "\x00" + item.ExternalID()
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			items = append(items, item)
			if len(items) >= g.maximumItems {
				return items, nil
			}
		}
		if len(page) == 0 || start+g.pageSize >= total {
			break
		}
	}
	return items, nil
}

func (g *Gateway) fetchPage(ctx context.Context, token string, start int) ([]wireItem, int, error) {
	requestURL := *g.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/library/sections/watchlist/all"
	query := requestURL.Query()
	query.Set("includeAdvanced", "1")
	query.Set("includeMeta", "1")
	query.Set("X-Plex-Container-Start", strconv.Itoa(start))
	query.Set("X-Plex-Container-Size", strconv.Itoa(g.pageSize))
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create Plex watchlist request: %w", ErrUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Plex-Token", token)
	request.Header.Set("X-Plex-Product", "BlackPearl")
	request.Header.Set("X-Plex-Version", "0.1.0")
	request.Header.Set("X-Plex-Client-Identifier", "blackpearl")
	response, err := g.client.Do(request)
	if err != nil {
		return nil, 0, sanitizedContextError(ctx, "send Plex watchlist request")
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(content) > maximumResponseSize {
		return nil, 0, fmt.Errorf("read Plex watchlist response: %w", ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, 0, domain.ErrUnauthorized
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, 0, fmt.Errorf("read Plex watchlist response: %w", ErrUnavailable)
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, 0, fmt.Errorf("decode Plex watchlist response: %w", ErrUnavailable)
	}
	if envelope.MediaContainer.TotalSize < 0 || envelope.MediaContainer.Size < 0 {
		return nil, 0, fmt.Errorf("validate Plex watchlist response: %w", ErrUnavailable)
	}
	return envelope.MediaContainer.Metadata, envelope.MediaContainer.TotalSize, nil
}

func sanitizedContextError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
