package torbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync/atomic"
)

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

type source struct {
	gateway    *Gateway
	identifier objectID
	metadata   fileMetadata
	download   atomic.Pointer[url.URL]
	closed     atomic.Bool
}

func newSource(gateway *Gateway, identifier objectID, metadata fileMetadata, downloadURL *url.URL) *source {
	result := &source{gateway: gateway, identifier: identifier, metadata: metadata}
	result.download.Store(downloadURL)
	return result
}

func (s *source) ReadAt(ctx context.Context, destination []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.closed.Load() {
		return 0, errors.New("TorBox range source is closed")
	}
	if offset < 0 {
		return 0, errors.New("TorBox range offset must not be negative")
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if offset >= s.metadata.size {
		return 0, io.EOF
	}
	wanted := int64(len(destination))
	partial := false
	if remaining := s.metadata.size - offset; wanted > remaining {
		wanted = remaining
		partial = true
	}
	count, status, err := s.readRange(ctx, s.download.Load(), destination[:wanted], offset, wanted)
	if err != nil && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusGone) {
		refreshed, refreshErr := s.gateway.downloadURL(ctx, s.identifier, s.metadata.validator, true)
		if refreshErr != nil {
			return 0, fmt.Errorf("refresh TorBox download link: %w", refreshErr)
		}
		s.download.Store(refreshed)
		count, _, err = s.readRange(ctx, refreshed, destination[:wanted], offset, wanted)
	}
	if err != nil {
		return count, err
	}
	if partial {
		return count, io.EOF
	}
	return count, nil
}

func (s *source) readRange(ctx context.Context, downloadURL *url.URL, destination []byte, offset int64, wanted int64) (count int, status int, resultErr error) {
	end := offset + wanted - 1
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("construct TorBox CDN range request: %w", err)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	response, err := s.gateway.client.Do(request)
	if err != nil {
		return 0, 0, errors.New("request TorBox CDN range")
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusPartialContent {
		return 0, response.StatusCode, fmt.Errorf("TorBox CDN range requires status 206: got %d", response.StatusCode)
	}
	if err := validateContentRange(response.Header.Get("Content-Range"), offset, end, s.metadata.size); err != nil {
		return 0, response.StatusCode, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, wanted+1))
	if err != nil {
		return 0, response.StatusCode, fmt.Errorf("read TorBox CDN range body: %w", err)
	}
	if int64(len(body)) != wanted {
		return 0, response.StatusCode, fmt.Errorf("TorBox CDN range body length mismatch: got %d want %d", len(body), wanted)
	}
	return copy(destination, body), response.StatusCode, nil
}

func (s *source) Size() int64 { return s.metadata.size }

func (s *source) Validator() string { return s.metadata.validator }

func (s *source) Close() error {
	s.closed.Store(true)
	return nil
}

var _ interface {
	ReadAt(context.Context, []byte, int64) (int, error)
	Size() int64
	Validator() string
	Close() error
} = (*source)(nil)

var _ = io.EOF

func validateContentRange(value string, expectedStart int64, expectedEnd int64, expectedSize int64) error {
	matches := contentRangePattern.FindStringSubmatch(value)
	if len(matches) != 4 {
		return fmt.Errorf("invalid TorBox CDN Content-Range: %q", value)
	}
	values := make([]int64, 3)
	for index, raw := range matches[1:] {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("parse TorBox CDN Content-Range %q: %w", value, err)
		}
		values[index] = parsed
	}
	if values[0] != expectedStart || values[1] != expectedEnd || values[2] != expectedSize {
		return fmt.Errorf("TorBox CDN Content-Range mismatch: got %d-%d/%d want %d-%d/%d", values[0], values[1], values[2], expectedStart, expectedEnd, expectedSize)
	}
	return nil
}
