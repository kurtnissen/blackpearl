package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

type deleteTorrentRequest struct {
	TorrentID int64  `json:"torrent_id"`
	Operation string `json:"operation"`
}

// DeleteCreatedTorrent deletes the exact TorBox torrent represented by created.
// It deliberately performs one request because retrying an ambiguous destructive
// operation could affect an object other than the one whose outcome is known.
func (g *Gateway) DeleteCreatedTorrent(ctx context.Context, created acquisition.CreatedObject) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("delete created TorBox torrent: %w", err)
	}
	if created.Provider() != providerName {
		return fmt.Errorf("delete created TorBox torrent: unsupported provider %q", created.Provider())
	}
	torrentID, err := strconv.ParseInt(created.ObjectID(), 10, 64)
	if err != nil || torrentID <= 0 {
		return errors.New("delete created TorBox torrent: object ID must be a positive integer")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/controltorrent")
	if err != nil {
		return errors.New("construct TorBox deletion URL")
	}
	body, err := json.Marshal(deleteTorrentRequest{TorrentID: torrentID, Operation: "delete"})
	if err != nil {
		return errors.New("encode TorBox deletion request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("construct TorBox deletion request")
	}
	request.Header.Set("Authorization", "Bearer "+g.token)
	request.Header.Set("Content-Type", "application/json")

	response, err := g.client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("perform TorBox deletion request: %w", contextErr)
		}
		return errors.New("perform TorBox deletion request")
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("TorBox API credentials rejected: %w", domain.ErrUnauthorized)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("TorBox deletion requires status 200: got %d", response.StatusCode)
	}
	var envelope apiEnvelope[json.RawMessage]
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBody+1))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode TorBox deletion response: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("TorBox deletion response exceeds one JSON value")
	}
	if !envelope.Success {
		return fmt.Errorf("TorBox deletion rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	return nil
}
