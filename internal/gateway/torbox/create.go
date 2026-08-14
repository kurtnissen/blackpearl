package torbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
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
	magnetQuery := url.Values{}
	magnetQuery.Set("xt", "urn:btih:"+release.InfoHash())
	input, err := acquisition.NewMagnetTorrentInput(release.InfoHash(), "magnet:?"+magnetQuery.Encode())
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("construct cached TorBox torrent input")
	}
	created, err := g.createTorrent(ctx, input, false)
	if err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("create cached TorBox torrent: %w", err)
	}
	return created, nil
}

// CreateTorrent creates one TorBox account object from validated transient
// material. allowDownload must be an explicit caller policy; the request is
// never retried automatically.
func (g *Gateway) CreateTorrent(ctx context.Context, input acquisition.TorrentInput, allowDownload bool) (acquisition.CreatedObject, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("create TorBox torrent: %w", err)
	}
	created, err := g.createTorrent(ctx, input, allowDownload)
	if err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("create TorBox torrent: %w", err)
	}
	return created, nil
}

func (g *Gateway) createTorrent(ctx context.Context, input acquisition.TorrentInput, allowDownload bool) (acquisition.CreatedObject, error) {
	if input.InfoHash() == "" || (input.Kind() != acquisition.TorrentInputMagnet && input.Kind() != acquisition.TorrentInputFile) {
		return acquisition.CreatedObject{}, errors.New("TorBox creation requires validated torrent material")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	switch input.Kind() {
	case acquisition.TorrentInputMagnet:
		if err := form.WriteField("magnet", input.Magnet()); err != nil {
			return acquisition.CreatedObject{}, errors.New("encode TorBox magnet creation form")
		}
	case acquisition.TorrentInputFile:
		part, err := form.CreateFormFile("file", "release.torrent")
		if err != nil {
			return acquisition.CreatedObject{}, errors.New("encode TorBox torrent file form")
		}
		if _, err := io.Copy(part, bytes.NewReader(input.File())); err != nil {
			return acquisition.CreatedObject{}, errors.New("write TorBox torrent file form")
		}
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "seed", value: "3"},
		{name: "allow_zip", value: "false"},
		{name: "as_queued", value: "false"},
		{name: "add_only_if_cached", value: strconv.FormatBool(!allowDownload)},
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
	if !strings.EqualFold(strings.TrimSpace(envelope.Data.Hash), input.InfoHash()) {
		return acquisition.CreatedObject{}, errors.New("TorBox returned a mismatched created torrent hash")
	}
	created, err := acquisition.NewCreatedObject(providerName, strconv.FormatInt(envelope.Data.TorrentID, 10))
	if err != nil {
		return acquisition.CreatedObject{}, errors.New("map created TorBox torrent")
	}
	return created, nil
}
