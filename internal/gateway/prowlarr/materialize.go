package prowlarr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// Materialize resolves one ephemeral Prowlarr torrent result into a bounded,
// info-hash-verified provider input. The returned material must never be
// persisted by callers.
func (g *Gateway) Materialize(ctx context.Context, release acquisition.Release) (_ acquisition.TorrentInput, resultErr error) {
	if err := ctx.Err(); err != nil {
		return acquisition.TorrentInput{}, fmt.Errorf("materialize Prowlarr release: %w", err)
	}
	if release.Provider() != providerName || release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" {
		return acquisition.TorrentInput{}, errors.New("prowlarr materialization requires a validated torrent release with info hash")
	}
	if release.MagnetURL() != "" {
		input, err := acquisition.NewMagnetTorrentInput(release.InfoHash(), release.MagnetURL())
		if err != nil {
			return acquisition.TorrentInput{}, errors.New("validate Prowlarr magnet material")
		}
		return input, nil
	}
	if release.DownloadURL() == "" {
		return acquisition.TorrentInput{}, errors.New("prowlarr release has no materializable locator")
	}
	download, err := url.Parse(release.DownloadURL())
	if err != nil || !g.allowedMaterialURL(download) {
		return acquisition.TorrentInput{}, errors.New("prowlarr material URL is outside the configured origin")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.String(), nil)
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("construct Prowlarr material request")
	}
	request.Header.Set("X-Api-Key", g.apiKey)
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream")
	response, err := g.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return acquisition.TorrentInput{}, fmt.Errorf("request Prowlarr material: %w", ctxErr)
		}
		return acquisition.TorrentInput{}, errors.New("request Prowlarr material")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Prowlarr material response"))
		}
	}()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return acquisition.TorrentInput{}, fmt.Errorf("prowlarr rejected material credentials: %w", domain.ErrUnauthorized)
	}
	if response.StatusCode != http.StatusOK {
		return acquisition.TorrentInput{}, fmt.Errorf("prowlarr material returned HTTP status %d", response.StatusCode)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType == "text/html" || mediaType == "application/json" || strings.HasPrefix(mediaType, "text/") {
		return acquisition.TorrentInput{}, errors.New("prowlarr material response is not a torrent file")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, acquisition.MaximumTorrentFileBytes+1))
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("read Prowlarr torrent material")
	}
	if len(payload) > acquisition.MaximumTorrentFileBytes {
		return acquisition.TorrentInput{}, fmt.Errorf("prowlarr torrent material exceeds %d bytes", acquisition.MaximumTorrentFileBytes)
	}
	input, err := acquisition.NewTorrentFileInput(release.InfoHash(), payload)
	if err != nil {
		return acquisition.TorrentInput{}, errors.New("validate Prowlarr torrent material")
	}
	return input, nil
}

func (g *Gateway) allowedMaterialURL(candidate *url.URL) bool {
	if candidate == nil || candidate.User != nil || candidate.Fragment != "" {
		return false
	}
	if !strings.EqualFold(candidate.Scheme, g.baseURL.Scheme) || !strings.EqualFold(candidate.Hostname(), g.baseURL.Hostname()) {
		return false
	}
	if effectivePort(candidate) != effectivePort(g.baseURL) {
		return false
	}
	basePath := strings.TrimSuffix(g.baseURL.EscapedPath(), "/")
	if basePath == "" {
		basePath = "/"
	}
	candidatePath := candidate.EscapedPath()
	if basePath != "/" && candidatePath != basePath && !strings.HasPrefix(candidatePath, basePath+"/") {
		return false
	}
	return true
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
