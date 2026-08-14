package torbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// InspectCreatedTorrent returns eligible video files from one freshly created
// account torrent. Callers may retry only ErrNotReady with their own bounded
// readiness policy.
func (g *Gateway) InspectCreatedTorrent(ctx context.Context, created acquisition.CreatedObject) ([]domain.MediaCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspect created TorBox torrent: %w", err)
	}
	if created.Provider() != providerName {
		return nil, fmt.Errorf("unsupported created TorBox provider: %q", created.Provider())
	}
	torrentID, err := strconv.ParseInt(created.ObjectID(), 10, 64)
	if err != nil || torrentID <= 0 || strconv.FormatInt(torrentID, 10) != created.ObjectID() {
		return nil, errors.New("created TorBox torrent ID must be a canonical positive integer")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/mylist")
	if err != nil {
		return nil, errors.New("construct created TorBox inspection URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("construct created TorBox inspection request")
	}
	query := request.URL.Query()
	query.Set("id", created.ObjectID())
	query.Set("bypass_cache", "true")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSONLimited(request, &envelope, maximumDiscoveryResponseBody); err != nil {
		return nil, fmt.Errorf("request created TorBox inspection: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("created TorBox inspection rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var torrent torrentRecord
	if err := json.Unmarshal(envelope.Data, &torrent); err != nil {
		var torrents []torrentRecord
		if listErr := json.Unmarshal(envelope.Data, &torrents); listErr != nil || len(torrents) != 1 {
			return nil, errors.New("decode created TorBox inspection data")
		}
		torrent = torrents[0]
	}
	if torrent.ID != torrentID {
		return nil, fmt.Errorf("created TorBox torrent not found: %w", domain.ErrNotFound)
	}
	if !torrent.DownloadFinished || !torrent.DownloadPresent {
		return nil, fmt.Errorf("created TorBox torrent is not ready: %w", acquisition.ErrNotReady)
	}
	result := candidatesFromTorrent(torrent)
	sortMediaCandidates(result)
	return result, nil
}
