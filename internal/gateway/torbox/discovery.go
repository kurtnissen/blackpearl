package torbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const maximumDiscoveryResponseBody = 8 << 20

// Discover returns completed, present account videos without requesting media bytes.
func (g *Gateway) Discover(ctx context.Context) ([]domain.MediaCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("discover TorBox media: %w", err)
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/mylist")
	if err != nil {
		return nil, errors.New("construct TorBox discovery URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("construct TorBox discovery request")
	}
	query := request.URL.Query()
	query.Set("bypass_cache", "false")
	query.Set("limit", "1000")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+g.token)
	var envelope apiEnvelope[json.RawMessage]
	if err := g.doJSONLimited(request, &envelope, maximumDiscoveryResponseBody); err != nil {
		return nil, fmt.Errorf("request TorBox discovery: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("TorBox discovery rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	var torrents []torrentRecord
	if err := json.Unmarshal(envelope.Data, &torrents); err != nil {
		return nil, errors.New("decode TorBox discovery data")
	}
	result := make([]domain.MediaCandidate, 0)
	for _, torrent := range torrents {
		if torrent.ID <= 0 || !torrent.DownloadFinished || !torrent.DownloadPresent {
			continue
		}
		for _, file := range torrent.Files {
			if file.ID <= 0 || file.Size <= 0 || file.Zipped || file.Infected || obviousSample(file.Name) {
				continue
			}
			if strings.TrimSpace(file.Hash) == "" && strings.TrimSpace(file.MD5) == "" {
				continue
			}
			candidate, candidateErr := domain.NewMediaCandidate(
				strconv.FormatInt(torrent.ID, 10)+":"+strconv.FormatInt(file.ID, 10),
				file.Name,
				file.Size,
			)
			if candidateErr != nil {
				continue
			}
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		leftName := strings.ToLower(result[left].Name)
		rightName := strings.ToLower(result[right].Name)
		if leftName == rightName {
			return result[left].ObjectID < result[right].ObjectID
		}
		return leftName < rightName
	})
	return result, nil
}

func obviousSample(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	base := path.Base(normalized)
	stem := strings.TrimSuffix(base, path.Ext(base))
	return stem == "sample" || strings.HasPrefix(stem, "sample-") || strings.HasPrefix(stem, "sample_") || strings.Contains(normalized, "/sample/")
}
