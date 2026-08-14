package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel"
)

const (
	maximumLibraryResponseBytes = 2 << 20
	maximumLibrarySections      = 256
	maximumPlexTokenBytes       = 4 << 10
)

// TokenSource supplies the current Plex token for one refresh attempt.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// LibraryRefresher discovers and refreshes Plex sections rooted at explicitly
// configured filesystem paths.
type LibraryRefresher struct {
	baseURL *url.URL
	tokens  TokenSource
	roots   map[string]struct{}
	client  *http.Client
}

type librarySectionsEnvelope struct {
	MediaContainer struct {
		Size        int                   `json:"size"`
		Directories []librarySectionEntry `json:"Directory"`
	} `json:"MediaContainer"`
}

type librarySectionEntry struct {
	Key       string            `json:"key"`
	Locations []libraryLocation `json:"Location"`
}

type libraryLocation struct {
	Path string `json:"path"`
}

// NewLibraryRefresher validates a Plex endpoint and exact library roots.
func NewLibraryRefresher(baseURL string, tokens TokenSource, roots []string, client *http.Client) (*LibraryRefresher, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Plex refresh URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if tokens == nil || client == nil {
		return nil, errors.New("Plex refresh token source and HTTP client are required")
	}
	configuredRoots := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if !strings.HasPrefix(root, "/") || strings.TrimSpace(root) != root || root == "/" {
			return nil, errors.New("Plex refresh roots must be absolute filesystem paths")
		}
		configuredRoots[root] = struct{}{}
	}
	if len(configuredRoots) == 0 {
		return nil, errors.New("at least one Plex refresh root is required")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &LibraryRefresher{baseURL: parsed, tokens: tokens, roots: configuredRoots, client: &isolatedClient}, nil
}

// Refresh discovers matching Plex sections and requests a scan of each one.
func (r *LibraryRefresher) Refresh(ctx context.Context) error {
	ctx, span := otel.Tracer("blackpearl/plex").Start(ctx, "plex.refresh_matching_libraries")
	defer span.End()
	token, err := r.tokens.Token(ctx)
	if err != nil {
		return sanitizedPlexContextError(ctx, "load Plex refresh credential")
	}
	if err := validateLibraryToken(token); err != nil {
		return err
	}
	sections, err := r.sections(ctx, token)
	if err != nil {
		return err
	}
	for _, key := range sections {
		if err := r.refreshSection(ctx, token, key); err != nil {
			return err
		}
	}
	return nil
}

func (r *LibraryRefresher) sections(ctx context.Context, token string) ([]string, error) {
	endpoint, err := url.JoinPath(r.baseURL.String(), "library", "sections")
	if err != nil {
		return nil, errors.New("construct Plex library sections request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("construct Plex library sections request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Plex-Token", token)
	content, err := r.do(request)
	if err != nil {
		return nil, err
	}
	var envelope librarySectionsEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, errors.New("decode Plex library sections response")
	}
	if envelope.MediaContainer.Size < 0 || envelope.MediaContainer.Size > maximumLibrarySections || envelope.MediaContainer.Size != len(envelope.MediaContainer.Directories) {
		return nil, errors.New("validate Plex library sections response")
	}
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for _, section := range envelope.MediaContainer.Directories {
		if !validLibrarySectionKey(section.Key) {
			return nil, errors.New("validate Plex library section key")
		}
		for _, location := range section.Locations {
			if _, matches := r.roots[location.Path]; !matches {
				continue
			}
			if _, exists := seen[section.Key]; !exists {
				seen[section.Key] = struct{}{}
				keys = append(keys, section.Key)
			}
			break
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("no matching BlackPearl Plex libraries are configured")
	}
	return keys, nil
}

func validateLibraryToken(token string) error {
	if token == "" || len(token) > maximumPlexTokenBytes || strings.TrimSpace(token) != token || strings.IndexFunc(token, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) >= 0 {
		return errors.New("validate Plex refresh credential")
	}
	return nil
}

func validLibrarySectionKey(key string) bool {
	if key == "" || len(key) > 20 {
		return false
	}
	for _, character := range key {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (r *LibraryRefresher) refreshSection(ctx context.Context, token string, key string) error {
	endpoint, err := url.JoinPath(r.baseURL.String(), "library", "sections", key, "refresh")
	if err != nil {
		return errors.New("construct Plex library refresh request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("construct Plex library refresh request")
	}
	request.Header.Set("X-Plex-Token", token)
	_, err = r.do(request)
	return err
}

func (r *LibraryRefresher) do(request *http.Request) (_ []byte, resultErr error) {
	response, err := r.client.Do(request)
	if err != nil {
		return nil, sanitizedPlexContextError(request.Context(), "request Plex library refresh")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Plex library refresh response"))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumLibraryResponseBytes+1))
	if err != nil || len(content) > maximumLibraryResponseBytes {
		return nil, errors.New("read Plex library refresh response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Plex library refresh returned HTTP status %d", response.StatusCode)
	}
	return content, nil
}

func sanitizedPlexContextError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return errors.New(operation)
}
