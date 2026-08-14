package torbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

// FindTorrentByHash reconciles a durable BitTorrent fingerprint with one
// existing TorBox account object without mutating provider state.
func (g *Gateway) FindTorrentByHash(ctx context.Context, infoHash string) (acquisition.CreatedObject, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("reconcile TorBox torrent: %w", err)
	}
	magnet := "magnet:?xt=urn:btih:" + infoHash
	validated, err := acquisition.NewMagnetTorrentInput(infoHash, magnet)
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("reconcile TorBox torrent requires a valid info hash")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/mylist")
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("construct TorBox reconciliation URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("construct TorBox reconciliation request")
	}
	query := request.URL.Query()
	query.Set("bypass_cache", "true")
	query.Set("limit", "1000")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSONLimited(request, &envelope, maximumDiscoveryResponseBody); err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("request TorBox reconciliation: %w", err)
	}
	if !envelope.Success {
		return acquisition.CreatedObject{}, fmt.Errorf("TorBox reconciliation rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var torrents []torrentRecord
	if err := json.Unmarshal(envelope.Data, &torrents); err != nil {
		var one torrentRecord
		if oneErr := json.Unmarshal(envelope.Data, &one); oneErr != nil {
			return acquisition.CreatedObject{}, errors.New("decode TorBox reconciliation data")
		}
		torrents = []torrentRecord{one}
	}
	matches := make([]torrentRecord, 0, 1)
	for _, torrent := range torrents {
		if torrent.ID > 0 && strings.EqualFold(strings.TrimSpace(torrent.Hash), validated.InfoHash()) {
			matches = append(matches, torrent)
		}
	}
	if len(matches) == 0 {
		return acquisition.CreatedObject{}, domain.ErrNotFound
	}
	if len(matches) > 1 {
		return acquisition.CreatedObject{}, acquisition.ErrAmbiguousProviderObjects
	}
	created, err := acquisition.NewCreatedObject(providerName, strconv.FormatInt(matches[0].ID, 10))
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("map reconciled TorBox torrent")
	}
	return created, nil
}
