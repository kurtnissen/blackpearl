package torbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
)

type createTorrentResponse struct {
	Hash      string `json:"hash"`
	TorrentID int64  `json:"torrent_id"`
}

// CreateCachedTorrent creates one TorBox account object with TorBox's
// authoritative cached-only guard enabled. It never retries a create request.
func (g *Gateway) CreateCachedTorrent(ctx context.Context, release acquisition.Release) (acquisition.CreatedObject, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("create cached TorBox torrent: %w", err)
	}
	if release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" {
		return acquisition.CreatedObject{}, errors.New("cached TorBox creation requires a validated torrent info hash")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	magnetQuery := url.Values{}
	magnetQuery.Set("xt", "urn:btih:"+release.InfoHash())
	fields := []struct {
		name  string
		value string
	}{
		{name: "magnet", value: "magnet:?" + magnetQuery.Encode()},
		{name: "seed", value: "3"},
		{name: "allow_zip", value: "false"},
		{name: "as_queued", value: "false"},
		{name: "add_only_if_cached", value: "true"},
	}
	for _, field := range fields {
		if err := form.WriteField(field.name, field.value); err != nil {
			return acquisition.CreatedObject{}, errors.New("encode cached TorBox creation form")
		}
	}
	if err := form.Close(); err != nil {
		return acquisition.CreatedObject{}, errors.New("finalize cached TorBox creation form")
	}
	endpoint, err := url.JoinPath(g.baseURL.String(), "torrents/createtorrent")
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("construct cached TorBox creation URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("construct cached TorBox creation request")
	}
	request.Header.Set("Authorization", "Bearer "+g.token)
	request.Header.Set("Content-Type", form.FormDataContentType())
	var envelope apiEnvelope[createTorrentResponse]
	if err := g.doJSON(request, &envelope); err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("request cached TorBox creation: %w", err)
	}
	if !envelope.Success {
		return acquisition.CreatedObject{}, fmt.Errorf("cached TorBox creation rejected: %s", g.sanitizeDetail(envelope.Detail))
	}
	if envelope.Data.TorrentID <= 0 {
		return acquisition.CreatedObject{}, errors.New("TorBox returned an invalid created torrent ID")
	}
	if !strings.EqualFold(strings.TrimSpace(envelope.Data.Hash), release.InfoHash()) {
		return acquisition.CreatedObject{}, errors.New("TorBox returned a mismatched created torrent hash")
	}
	created, err := acquisition.NewCreatedObject(providerName, strconv.FormatInt(envelope.Data.TorrentID, 10))
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("map created TorBox torrent")
	}
	return created, nil
}
