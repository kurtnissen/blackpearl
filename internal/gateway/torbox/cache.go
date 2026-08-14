package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
)

const maximumCacheLookupItems = 100

type cacheLookupRequest struct {
	Hashes []string `json:"hashes"`
}

type cachedTorrentRecord struct {
	Hash string `json:"hash"`
}

// CachedTorrents returns eligible input releases that TorBox currently reports
// as cached, preserving input rank order and removing duplicate hashes.
func (g *Gateway) CachedTorrents(ctx context.Context, releases []acquisition.Release) ([]acquisition.Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check TorBox torrent cache: %w", err)
	}
	unique := make([]acquisition.Release, 0, len(releases))
	hashes := make([]string, 0, len(releases))
	requested := make(map[string]struct{}, len(releases))
	for _, release := range releases {
		if release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" {
			continue
		}
		hash := strings.ToLower(release.InfoHash())
		if _, exists := requested[hash]; exists {
			continue
		}
		if len(hashes) == maximumCacheLookupItems {
			return nil, fmt.Errorf("TorBox cache lookup supports at most %d unique torrent hashes", maximumCacheLookupItems)
		}
		requested[hash] = struct{}{}
		hashes = append(hashes, release.InfoHash())
		unique = append(unique, release)
	}
	if len(hashes) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(cacheLookupRequest{Hashes: hashes})
	if err != nil {
		return nil, errors.New("encode TorBox cache lookup")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/checkcached")
	if err != nil {
		return nil, errors.New("construct TorBox cache lookup URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("construct TorBox cache lookup request")
	}
	query := request.URL.Query()
	query.Set("format", "object")
	query.Set("list_files", "false")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	request.Header.Set("Content-Type", "application/json")
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSONLimited(request, &envelope, maximumDiscoveryResponseBody); err != nil {
		return nil, fmt.Errorf("request TorBox cache lookup: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("TorBox cache lookup rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var records map[string]cachedTorrentRecord
	if err := json.Unmarshal(envelope.Data, &records); err != nil {
		return nil, errors.New("decode TorBox cache lookup data")
	}
	cachedHashes := make(map[string]struct{}, len(records))
	for key, record := range records {
		hash := strings.ToLower(strings.TrimSpace(record.Hash))
		if hash == "" {
			hash = strings.ToLower(strings.TrimSpace(key))
		}
		if _, exists := requested[hash]; exists {
			cachedHashes[hash] = struct{}{}
		}
	}
	result := make([]acquisition.Release, 0, len(cachedHashes))
	for _, release := range unique {
		if _, cached := cachedHashes[strings.ToLower(release.InfoHash())]; cached {
			result = append(result, release)
		}
	}
	return result, nil
}
