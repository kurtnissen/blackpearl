package torbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

// InspectCreatedTorrent returns eligible video files from one freshly created
// account torrent. Callers may retry only ErrNotReady with their own bounded
// readiness policy.
func (g *Gateway) InspectCreatedTorrent(ctx context.Context, created acquisition.CreatedObject) (acquisition.PreparationInspection, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.PreparationInspection{}, fmt.Errorf("inspect created TorBox torrent: %w", err)
	}
	if created.Provider() != providerName {
		return acquisition.PreparationInspection{}, fmt.Errorf("unsupported created TorBox provider: %q", created.Provider())
	}
	torrentID, err := strconv.ParseInt(created.ObjectID(), 10, 64)
	if err != nil || torrentID <= 0 || strconv.FormatInt(torrentID, 10) != created.ObjectID() {
		return acquisition.PreparationInspection{}, errors.New("created TorBox torrent ID must be a canonical positive integer")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/mylist")
	if err != nil {
		return acquisition.PreparationInspection{}, errors.New("construct created TorBox inspection URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return acquisition.PreparationInspection{}, errors.New("construct created TorBox inspection request")
	}
	query := request.URL.Query()
	query.Set("id", created.ObjectID())
	query.Set("bypass_cache", "true")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSONLimited(request, &envelope, maximumDiscoveryResponseBody); err != nil {
		return acquisition.PreparationInspection{}, fmt.Errorf("request created TorBox inspection: %w", err)
	}
	if !envelope.Success {
		return acquisition.PreparationInspection{}, fmt.Errorf("created TorBox inspection rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var torrent torrentRecord
	if err := json.Unmarshal(envelope.Data, &torrent); err != nil {
		var torrents []torrentRecord
		if listErr := json.Unmarshal(envelope.Data, &torrents); listErr != nil {
			return acquisition.PreparationInspection{}, errors.New("decode created TorBox inspection data")
		}
		if len(torrents) == 0 {
			return acquisition.PreparationInspection{}, fmt.Errorf("created TorBox torrent not found: %w", domain.ErrNotFound)
		}
		if len(torrents) != 1 {
			return acquisition.PreparationInspection{}, fmt.Errorf("created TorBox inspection returned multiple objects: %w", acquisition.ErrAmbiguousProviderObjects)
		}
		torrent = torrents[0]
	}
	if torrent.ID != torrentID {
		return acquisition.PreparationInspection{}, fmt.Errorf("created TorBox torrent not found: %w", domain.ErrNotFound)
	}
	progress, err := torBoxPreparationProgress(torrent)
	if err != nil {
		return acquisition.PreparationInspection{}, err
	}
	if !torrent.DownloadFinished || !torrent.DownloadPresent {
		inspection, inspectionErr := acquisition.NewPreparationInspection(nil, progress)
		if inspectionErr != nil {
			return acquisition.PreparationInspection{}, fmt.Errorf("validate created TorBox inspection: %w", inspectionErr)
		}
		if !torrent.DownloadFinished && strings.HasPrefix(strings.ToLower(strings.TrimSpace(torrent.DownloadState)), "stalled") {
			return inspection, fmt.Errorf("created TorBox torrent has no available source: %w", acquisition.ErrStalled)
		}
		return inspection, fmt.Errorf("created TorBox torrent is not ready: %w", acquisition.ErrNotReady)
	}
	result := candidatesFromTorrent(torrent)
	sortMediaCandidates(result)
	inspection, err := acquisition.NewPreparationInspection(result, 100)
	if err != nil {
		return acquisition.PreparationInspection{}, fmt.Errorf("validate created TorBox inspection: %w", err)
	}
	return inspection, nil
}

func torBoxPreparationProgress(torrent torrentRecord) (int, error) {
	if math.IsNaN(torrent.Progress) || math.IsInf(torrent.Progress, 0) || torrent.Progress < 0 || torrent.Progress > 1 {
		return 0, errors.New("created TorBox torrent progress must be between zero and one")
	}
	if torrent.DownloadFinished && torrent.DownloadPresent {
		return 100, nil
	}
	return min(99, int(math.Floor(torrent.Progress*100))), nil
}
