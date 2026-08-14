// Package plexmetadata resolves exact next-episode coordinates from Plex metadata.
package plexmetadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const (
	maximumResponseBytes = 2 << 20
	maximumSeasons       = 100
	maximumEpisodes      = 1000
	maximumTokenBytes    = 4 << 10
	maximumPageSize      = 100
)

var (
	showGUIDPattern   = regexp.MustCompile(`^plex://show/([0-9a-f]{24})$`)
	metadataIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)
	// ErrUnavailable indicates that Plex metadata could not be read safely.
	ErrUnavailable = errors.New("plex metadata unavailable")
)

// TokenSource supplies the current Plex token for one resolution.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Gateway reads bounded Plex show hierarchy metadata.
type Gateway struct {
	baseURL *url.URL
	tokens  TokenSource
	client  *http.Client
}

type wireEnvelope struct {
	MediaContainer wireContainer `json:"MediaContainer"`
}

type wireContainer struct {
	Size      int         `json:"size"`
	TotalSize *int        `json:"totalSize"`
	Offset    *int        `json:"offset"`
	Metadata  []wireEntry `json:"Metadata"`
}

type wireEntry struct {
	Type        string `json:"type"`
	Index       int    `json:"index"`
	ParentIndex int    `json:"parentIndex"`
	RatingKey   string `json:"ratingKey"`
}

type seasonRef struct {
	index int
	id    string
}

// New validates and constructs the metadata gateway.
func New(baseURL string, tokens TokenSource, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("plex metadata URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if tokens == nil || client == nil {
		return nil, errors.New("plex metadata token source and HTTP client are required")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{baseURL: parsed, tokens: tokens, client: &isolatedClient}, nil
}

// Next returns the least validated episode coordinate strictly after current.
func (g *Gateway) Next(ctx context.Context, externalShowID string, current domain.EpisodeCoordinate) (domain.EpisodeCoordinate, error) {
	ctx, span := otel.Tracer("blackpearl/plexmetadata").Start(ctx, "plex_metadata.next_episode")
	defer span.End()
	if err := ctx.Err(); err != nil {
		return domain.EpisodeCoordinate{}, fmt.Errorf("resolve Plex next episode: %w", err)
	}
	showMatches := showGUIDPattern.FindStringSubmatch(externalShowID)
	if len(showMatches) != 2 {
		return domain.EpisodeCoordinate{}, errors.New("plex show identity is invalid")
	}
	if _, err := domain.NewEpisodeCoordinate(current.Season(), current.Episode()); err != nil {
		return domain.EpisodeCoordinate{}, fmt.Errorf("current episode coordinate is invalid: %w", err)
	}
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return domain.EpisodeCoordinate{}, metadataContextError(ctx, "load Plex metadata credential")
	}
	if err := validateToken(token); err != nil {
		return domain.EpisodeCoordinate{}, fmt.Errorf("load Plex metadata credential: %w", ErrUnavailable)
	}
	seasons, err := g.seasons(ctx, token, showMatches[1])
	if err != nil {
		return domain.EpisodeCoordinate{}, err
	}
	currentFound := false
	for _, season := range seasons {
		if season.index == current.Season() {
			currentFound = true
			break
		}
	}
	if !currentFound {
		return domain.EpisodeCoordinate{}, domain.ErrNotFound
	}
	for _, season := range seasons {
		if season.index < current.Season() {
			continue
		}
		episodes, episodeErr := g.episodes(ctx, token, season)
		if episodeErr != nil {
			return domain.EpisodeCoordinate{}, episodeErr
		}
		if season.index == current.Season() {
			currentEpisodeFound := false
			for _, episode := range episodes {
				if episode == current.Episode() {
					currentEpisodeFound = true
					break
				}
			}
			if !currentEpisodeFound {
				return domain.EpisodeCoordinate{}, domain.ErrNotFound
			}
		}
		for _, episode := range episodes {
			coordinate, coordinateErr := domain.NewEpisodeCoordinate(season.index, episode)
			if coordinateErr == nil && coordinate.After(current) {
				return coordinate, nil
			}
		}
	}
	return domain.EpisodeCoordinate{}, domain.ErrNotFound
}

func (g *Gateway) seasons(ctx context.Context, token string, showID string) ([]seasonRef, error) {
	entries, err := g.children(ctx, token, showID, maximumSeasons)
	if err != nil {
		return nil, err
	}
	result := make([]seasonRef, 0, len(entries))
	seen := make(map[int]string)
	for _, entry := range entries {
		if entry.Type != "season" || entry.Index < 1 || entry.Index > 99 || !metadataIDPattern.MatchString(entry.RatingKey) {
			continue
		}
		if existing, duplicate := seen[entry.Index]; duplicate {
			if existing != entry.RatingKey {
				return nil, fmt.Errorf("validate Plex season metadata: %w", ErrUnavailable)
			}
			continue
		}
		seen[entry.Index] = entry.RatingKey
		result = append(result, seasonRef{index: entry.Index, id: entry.RatingKey})
	}
	sort.Slice(result, func(left int, right int) bool { return result[left].index < result[right].index })
	return result, nil
}

func (g *Gateway) episodes(ctx context.Context, token string, season seasonRef) ([]int, error) {
	entries, err := g.children(ctx, token, season.id, maximumEpisodes)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{})
	result := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "episode" || entry.ParentIndex != season.index || entry.Index < 1 || entry.Index > 999 {
			continue
		}
		if _, duplicate := seen[entry.Index]; duplicate {
			continue
		}
		seen[entry.Index] = struct{}{}
		result = append(result, entry.Index)
	}
	sort.Ints(result)
	return result, nil
}

func (g *Gateway) children(ctx context.Context, token string, metadataID string, maximum int) ([]wireEntry, error) {
	if !metadataIDPattern.MatchString(metadataID) {
		return nil, fmt.Errorf("validate Plex metadata identity: %w", ErrUnavailable)
	}
	result := make([]wireEntry, 0)
	for start := 0; ; {
		remaining := maximum - len(result)
		if remaining <= 0 {
			return nil, fmt.Errorf("validate Plex metadata response: %w", ErrUnavailable)
		}
		requestSize := min(maximumPageSize, remaining)
		page, offset, total, paginated, err := g.childrenPage(ctx, token, metadataID, start, requestSize, maximum)
		if err != nil {
			return nil, err
		}
		if !paginated {
			if start != 0 || len(page) > maximum {
				return nil, fmt.Errorf("validate Plex metadata pagination: %w", ErrUnavailable)
			}
			return page, nil
		}
		if offset != start || total > maximum || len(page) == 0 || offset+len(page) > total {
			return nil, fmt.Errorf("validate Plex metadata pagination: %w", ErrUnavailable)
		}
		result = append(result, page...)
		next := offset + len(page)
		if next == total {
			return result, nil
		}
		start = next
	}
}

func (g *Gateway) childrenPage(
	ctx context.Context,
	token string,
	metadataID string,
	start int,
	requestSize int,
	maximum int,
) ([]wireEntry, int, int, bool, error) {
	endpoint, err := url.JoinPath(g.baseURL.String(), "library", "metadata", metadataID, "children")
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("construct Plex metadata request: %w", ErrUnavailable)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("construct Plex metadata request: %w", ErrUnavailable)
	}
	query := parsed.Query()
	query.Set("includeMeta", "1")
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("construct Plex metadata request: %w", ErrUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Plex-Token", token)
	request.Header.Set("X-Plex-Container-Start", strconv.Itoa(start))
	request.Header.Set("X-Plex-Container-Size", strconv.Itoa(requestSize))
	content, responseHeaders, err := g.do(request)
	if err != nil {
		return nil, 0, 0, false, err
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, 0, 0, false, fmt.Errorf("decode Plex metadata response: %w", ErrUnavailable)
	}
	container := envelope.MediaContainer
	if container.Size < 0 || container.Size > maximum || container.Size != len(container.Metadata) {
		return nil, 0, 0, false, fmt.Errorf("validate Plex metadata response: %w", ErrUnavailable)
	}
	offset, hasOffset, err := paginationInteger(container.Offset, responseHeaders.Get("X-Plex-Container-Start"))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("validate Plex metadata pagination: %w", ErrUnavailable)
	}
	total, hasTotal, err := paginationInteger(container.TotalSize, responseHeaders.Get("X-Plex-Container-Total-Size"))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("validate Plex metadata pagination: %w", ErrUnavailable)
	}
	if !hasOffset && !hasTotal {
		return container.Metadata, 0, 0, false, nil
	}
	if !hasOffset || !hasTotal || offset < 0 || total < 0 || offset > total {
		return nil, 0, 0, false, fmt.Errorf("validate Plex metadata pagination: %w", ErrUnavailable)
	}
	return container.Metadata, offset, total, true, nil
}

func paginationInteger(bodyValue *int, headerValue string) (int, bool, error) {
	if bodyValue == nil && headerValue == "" {
		return 0, false, nil
	}
	if headerValue == "" {
		return *bodyValue, true, nil
	}
	parsed, err := strconv.Atoi(headerValue)
	if err != nil {
		return 0, false, err
	}
	if bodyValue != nil && *bodyValue != parsed {
		return 0, false, errors.New("plex pagination header and body disagree")
	}
	return parsed, true, nil
}

func (g *Gateway) do(request *http.Request) (_ []byte, _ http.Header, resultErr error) {
	response, err := g.client.Do(request)
	if err != nil {
		return nil, nil, metadataContextError(request.Context(), "request Plex metadata")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close Plex metadata response: %w", ErrUnavailable))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(content) > maximumResponseBytes {
		return nil, nil, fmt.Errorf("read Plex metadata response: %w", ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, nil, domain.ErrUnauthorized
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("read Plex metadata response: %w", ErrUnavailable)
	}
	return content, response.Header.Clone(), nil
}

func validateToken(token string) error {
	if token == "" || len(token) > maximumTokenBytes || strings.TrimSpace(token) != token {
		return errors.New("invalid Plex metadata credential")
	}
	for _, character := range token {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return errors.New("invalid Plex metadata credential")
		}
	}
	return nil
}

func metadataContextError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
