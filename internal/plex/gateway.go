// Package plex implements BlackPearl's narrow Plex HTTP gateway.
package plex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel"
)

const maximumErrorBodyBytes = 4 * 1024

// Gateway refreshes one explicitly configured Plex library.
type Gateway struct {
	baseURL   *url.URL
	token     string
	sectionID string
	client    *http.Client
}

// New validates Plex connection settings.
func New(baseURL string, token string, sectionID string, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("plex base URL must be an absolute HTTP URL: %q", baseURL)
	}
	if token == "" {
		return nil, errors.New("plex token is required")
	}
	if sectionID == "" {
		return nil, errors.New("plex section ID is required")
	}
	if client == nil {
		return nil, errors.New("plex HTTP client is required")
	}
	return &Gateway{baseURL: parsed, token: token, sectionID: sectionID, client: client}, nil
}

// Refresh requests a scan of the configured Plex library section.
func (g *Gateway) Refresh(ctx context.Context) error {
	ctx, span := otel.Tracer("blackpearl/plex").Start(ctx, "plex.refresh_library")
	defer span.End()
	requestURL := strings.TrimRight(g.baseURL.String(), "/") +
		"/library/sections/" + url.PathEscape(g.sectionID) + "/refresh"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("create Plex refresh request: %w", err)
	}
	request.Header.Set("X-Plex-Token", g.token)
	response, err := g.client.Do(request)
	if err != nil {
		return fmt.Errorf("send Plex refresh request: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximumErrorBodyBytes))
	closeErr := response.Body.Close()
	if readErr != nil {
		if closeErr != nil {
			return errors.Join(
				fmt.Errorf("read Plex refresh response: %w", readErr),
				fmt.Errorf("close Plex refresh response: %w", closeErr),
			)
		}
		return fmt.Errorf("read Plex refresh response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Plex refresh response: %w", closeErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		sanitizedBody := strings.Map(func(character rune) rune {
			if unicode.IsPrint(character) {
				return character
			}
			return '?'
		}, string(body))
		return fmt.Errorf("plex refresh returned status %d: %s", response.StatusCode, sanitizedBody)
	}
	return nil
}
