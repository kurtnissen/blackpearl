package prowlarr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/kurtnissen/blackpearl/internal/domain"
)

const maximumReadyBodyBytes = 1 << 20

// Ready verifies the configured Prowlarr endpoint and API key without running
// an indexer search.
func (g *Gateway) Ready(ctx context.Context) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("probe Prowlarr: %w", err)
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "api/v1/health")
	if err != nil {
		return errors.New("construct Prowlarr health URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("construct Prowlarr health request")
	}
	request.Header.Set("X-Api-Key", g.apiKey)
	response, err := g.client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("request Prowlarr health: %w", contextErr)
		}
		return errors.New("request Prowlarr health")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Prowlarr health response"))
		}
	}()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("prowlarr rejected API credentials: %w", domain.ErrUnauthorized)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("prowlarr health returned HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumReadyBodyBytes+1))
	if err != nil {
		return errors.New("read Prowlarr health response")
	}
	if len(body) > maximumReadyBodyBytes {
		return errors.New("prowlarr health response exceeds 1 MiB")
	}
	return nil
}
