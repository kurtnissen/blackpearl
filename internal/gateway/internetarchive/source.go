package internetarchive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

var archiveContentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

// Open validates exact-file metadata without downloading content bytes.
func (g *Gateway) Open(ctx context.Context, backing domain.BackingRef) (_ acquisition.RangeSource, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open Internet Archive range source: %w", err)
	}
	if backing.Provider != FileProviderName {
		return nil, fmt.Errorf("unsupported Internet Archive range provider: %q", backing.Provider)
	}
	identifier, filename, err := decodeFileObjectID(backing.ObjectID)
	if err != nil {
		return nil, err
	}
	metadata, err := g.fetchMetadata(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if !supportedLicense(metadata.Metadata.LicenseURL) {
		return nil, errors.New("internet Archive item no longer declares a supported open license")
	}
	file, err := findExactFile(metadata.Files, filename)
	if err != nil {
		return nil, err
	}
	downloadURL, err := g.fileDownloadURL(metadata, identifier, filename)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
	if err != nil {
		return nil, errors.New("construct Internet Archive file metadata request")
	}
	response, err := g.downloadClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("request Internet Archive file metadata: %w", ctxErr)
		}
		return nil, errors.New("request Internet Archive file metadata")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Internet Archive file metadata response"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("internet Archive file metadata requires status 200: got %d", response.StatusCode)
	}
	if response.ContentLength != file.size {
		return nil, fmt.Errorf("internet Archive file size changed: got %d want %d", response.ContentLength, file.size)
	}
	etag := strings.TrimSpace(response.Header.Get("ETag"))
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	lastModified := strings.TrimSpace(response.Header.Get("Last-Modified"))
	if etag == "" && lastModified == "" {
		return nil, errors.New("internet Archive file metadata requires a strong ETag or Last-Modified validator")
	}
	return &Source{
		client: g.downloadClient, objectURL: downloadURL, size: file.size, sha1: file.sha1,
		etag: etag, lastModified: lastModified,
	}, nil
}

// Source is one licensed Archive file capable of exact random reads.
type Source struct {
	client       *http.Client
	objectURL    string
	size         int64
	sha1         string
	etag         string
	lastModified string
	closed       atomic.Bool
}

// Size returns the remote logical file size.
func (s *Source) Size() int64 { return s.size }

// Validator returns the metadata SHA-1 identity without exposing a source URL.
func (s *Source) Validator() string { return "internet-archive:sha1:" + s.sha1 }

// ReadAt performs one strict HTTP range read.
func (s *Source) ReadAt(ctx context.Context, destination []byte, offset int64) (count int, resultErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.closed.Load() {
		return 0, errors.New("Internet Archive range source is closed")
	}
	if offset < 0 {
		return 0, errors.New("Internet Archive range offset must not be negative")
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if offset >= s.size {
		return 0, io.EOF
	}
	wanted := int64(len(destination))
	partial := false
	if remaining := s.size - offset; wanted > remaining {
		wanted = remaining
		partial = true
	}
	end := offset + wanted - 1
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL, nil)
	if err != nil {
		return 0, errors.New("construct Internet Archive range request")
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	if s.etag != "" {
		request.Header.Set("If-Range", s.etag)
	} else {
		request.Header.Set("If-Range", s.lastModified)
	}
	response, err := s.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, errors.New("request Internet Archive file range")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close Internet Archive range response"))
		}
	}()
	if response.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("internet Archive range requires status 206: got %d", response.StatusCode)
	}
	if err := validateArchiveContentRange(response.Header.Get("Content-Range"), offset, end, s.size); err != nil {
		return 0, err
	}
	if s.etag != "" && response.Header.Get("ETag") != s.etag {
		return 0, errors.New("internet Archive file HTTP validator changed")
	}
	if s.etag == "" && response.Header.Get("Last-Modified") != s.lastModified {
		return 0, errors.New("internet Archive file HTTP validator changed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, wanted+1))
	if err != nil {
		return 0, errors.New("read Internet Archive range body")
	}
	if int64(len(body)) != wanted {
		return 0, fmt.Errorf("internet Archive range body length mismatch: got %d want %d", len(body), wanted)
	}
	count = copy(destination, body)
	if partial {
		return count, io.EOF
	}
	return count, nil
}

// Close prevents future reads.
func (s *Source) Close() error {
	s.closed.Store(true)
	return nil
}

func validateArchiveContentRange(value string, expectedStart int64, expectedEnd int64, expectedSize int64) error {
	matches := archiveContentRangePattern.FindStringSubmatch(value)
	if len(matches) != 4 {
		return errors.New("invalid Internet Archive Content-Range")
	}
	values := make([]int64, 3)
	for index, raw := range matches[1:] {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return errors.New("invalid Internet Archive Content-Range")
		}
		values[index] = parsed
	}
	if values[0] != expectedStart || values[1] != expectedEnd || values[2] != expectedSize {
		return fmt.Errorf(
			"Internet Archive Content-Range mismatch: got %d-%d/%d want %d-%d/%d",
			values[0], values[1], values[2], expectedStart, expectedEnd, expectedSize,
		)
	}
	return nil
}

var _ acquisition.RangeSource = (*Source)(nil)
