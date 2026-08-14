// Package plexplayback reads bounded active-session evidence from a local Plex server.
package plexplayback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const (
	maximumResponseBytes = 2 << 20
	maximumSessions      = 64
	maximumTokenBytes    = 4 << 10
)

// ErrUnavailable indicates that Plex playback evidence could not be read safely.
var ErrUnavailable = errors.New("plex playback unavailable")

// TokenSource supplies the current Plex token for one snapshot.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Options configures the local Plex playback boundary.
type Options struct {
	BaseURL     string
	LibraryRoot string
}

// Gateway reads sanitized active episode playback evidence.
type Gateway struct {
	baseURL     *url.URL
	libraryRoot string
	tokens      TokenSource
	client      *http.Client
}

type wireEnvelope struct {
	MediaContainer wireContainer `json:"MediaContainer"`
}

type wireContainer struct {
	Size     int        `json:"size"`
	Metadata []wireItem `json:"Metadata"`
}

type wireItem struct {
	Type            string      `json:"type"`
	GrandparentGUID string      `json:"grandparentGuid"`
	ParentIndex     int         `json:"parentIndex"`
	Index           int         `json:"index"`
	ViewOffset      int64       `json:"viewOffset"`
	Duration        int64       `json:"duration"`
	Player          wirePlayer  `json:"Player"`
	Media           []wireMedia `json:"Media"`
}

type wirePlayer struct {
	State string `json:"state"`
}

type wireMedia struct {
	Parts []wirePart `json:"Part"`
}

type wirePart struct {
	File     string `json:"file"`
	Selected bool   `json:"selected"`
}

// New validates and constructs the playback gateway.
func New(options Options, tokens TokenSource, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("plex playback URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	root := strings.TrimSpace(options.LibraryRoot)
	if root == "" || root == "/" || !path.IsAbs(root) || path.Clean(root) != root || strings.Contains(root, "\\") {
		return nil, errors.New("plex playback library root must be one clean absolute path")
	}
	if tokens == nil || client == nil {
		return nil, errors.New("plex playback token source and HTTP client are required")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{baseURL: parsed, libraryRoot: root, tokens: tokens, client: &isolatedClient}, nil
}

// Snapshot returns normalized BlackPearl episode sessions only.
func (g *Gateway) Snapshot(ctx context.Context) ([]domain.EpisodePlayback, error) {
	ctx, span := otel.Tracer("blackpearl/plexplayback").Start(ctx, "plex_playback.snapshot")
	defer span.End()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read Plex playback: %w", err)
	}
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return nil, playbackContextError(ctx, "load Plex playback credential")
	}
	if err := validateToken(token); err != nil {
		return nil, fmt.Errorf("load Plex playback credential: %w", ErrUnavailable)
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "status", "sessions")
	if err != nil {
		return nil, fmt.Errorf("construct Plex playback request: %w", ErrUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("construct Plex playback request: %w", ErrUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Plex-Token", token)
	content, err := g.do(request)
	if err != nil {
		return nil, err
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, fmt.Errorf("decode Plex playback response: %w", ErrUnavailable)
	}
	container := envelope.MediaContainer
	if container.Size < 0 || container.Size > maximumSessions || container.Size != len(container.Metadata) {
		return nil, fmt.Errorf("validate Plex playback response: %w", ErrUnavailable)
	}
	result := make([]domain.EpisodePlayback, 0, len(container.Metadata))
	for _, item := range container.Metadata {
		playback, ok := g.normalize(item)
		if ok {
			result = append(result, playback)
		}
	}
	return result, nil
}

func (g *Gateway) normalize(item wireItem) (domain.EpisodePlayback, bool) {
	if item.Type != "episode" {
		return domain.EpisodePlayback{}, false
	}
	selected := ""
	selectedCount := 0
	for _, media := range item.Media {
		for _, part := range media.Parts {
			if part.Selected {
				selected = part.File
				selectedCount++
			}
		}
	}
	if selectedCount != 1 {
		return domain.EpisodePlayback{}, false
	}
	prefix := g.libraryRoot + "/"
	if !strings.HasPrefix(selected, prefix) || path.Clean(selected) != selected {
		return domain.EpisodePlayback{}, false
	}
	virtualPath := strings.TrimPrefix(selected, prefix)
	playback, err := domain.NewEpisodePlayback(
		item.GrandparentGUID, virtualPath, item.ParentIndex, item.Index,
		time.Duration(item.ViewOffset)*time.Millisecond,
		time.Duration(item.Duration)*time.Millisecond,
		domain.PlaybackState(item.Player.State),
	)
	return playback, err == nil
}

func (g *Gateway) do(request *http.Request) (_ []byte, resultErr error) {
	response, err := g.client.Do(request)
	if err != nil {
		return nil, playbackContextError(request.Context(), "request Plex playback")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close Plex playback response: %w", ErrUnavailable))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(content) > maximumResponseBytes {
		return nil, fmt.Errorf("read Plex playback response: %w", ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, domain.ErrUnauthorized
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("read Plex playback response: %w", ErrUnavailable)
	}
	return content, nil
}

func validateToken(token string) error {
	if token == "" || len(token) > maximumTokenBytes || strings.TrimSpace(token) != token {
		return errors.New("invalid Plex playback credential")
	}
	for _, character := range token {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return errors.New("invalid Plex playback credential")
		}
	}
	return nil
}

func playbackContextError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
