// Package httporigin exposes explicitly configured HTTP objects as strict
// provider-neutral range sources.
package httporigin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

const providerName = "http-range"

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

// Gateway maps one explicitly configured HTTP origin to range sources.
type Gateway struct {
	baseURL *url.URL
	client  *http.Client
}

// New validates an HTTP origin gateway.
func New(baseURL string, client *http.Client) (*Gateway, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("range origin must be an absolute HTTP URL: %q", baseURL)
	}
	if client == nil {
		return nil, errors.New("range origin HTTP client is required")
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Gateway{baseURL: parsed, client: &isolatedClient}, nil
}

// Ready verifies that the gateway configuration remains usable by the caller.
// Object availability is established by Open because the gateway is not tied
// to one object identifier.
func (g *Gateway) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check range origin readiness: %w", err)
	}
	return nil
}

// Open retrieves immutable object metadata without downloading object bytes.
func (g *Gateway) Open(ctx context.Context, backing domain.BackingRef) (_ acquisition.RangeSource, openErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open HTTP range source: %w", err)
	}
	if backing.Provider != providerName {
		return nil, fmt.Errorf("unsupported range provider: %q", backing.Provider)
	}
	if err := validateObjectID(backing.ObjectID); err != nil {
		return nil, err
	}
	objectURL, err := url.JoinPath(g.baseURL.String(), backing.ObjectID)
	if err != nil {
		return nil, fmt.Errorf("construct range object URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, objectURL, nil)
	if err != nil {
		return nil, fmt.Errorf("construct range metadata request: %w", err)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request range object metadata: %w", err)
	}
	defer func() {
		openErr = errors.Join(openErr, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("range metadata requires status 200: got %d", response.StatusCode)
	}
	if response.ContentLength <= 0 {
		return nil, fmt.Errorf("range metadata requires positive Content-Length: %d", response.ContentLength)
	}
	etag := strings.TrimSpace(response.Header.Get("ETag"))
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	lastModified := strings.TrimSpace(response.Header.Get("Last-Modified"))
	if etag == "" && lastModified == "" {
		return nil, errors.New("range metadata requires a strong ETag or Last-Modified validator")
	}
	return &Source{
		client:       g.client,
		objectURL:    objectURL,
		size:         response.ContentLength,
		etag:         etag,
		lastModified: lastModified,
	}, nil
}

// Source is one immutable HTTP object capable of exact random reads.
type Source struct {
	client       *http.Client
	objectURL    string
	size         int64
	etag         string
	lastModified string
	closed       atomic.Bool
}

// Size returns the remote object's immutable logical size.
func (s *Source) Size() int64 {
	return s.size
}

// Validator identifies the immutable version opened by this source.
func (s *Source) Validator() string {
	if s.etag != "" {
		return "etag:" + s.etag
	}
	if s.lastModified != "" {
		return "last-modified:" + s.lastModified
	}
	return ""
}

// ReadAt retrieves exactly the requested available HTTP byte range.
func (s *Source) ReadAt(ctx context.Context, destination []byte, offset int64) (count int, readErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.closed.Load() {
		return 0, errors.New("HTTP range source is closed")
	}
	if offset < 0 {
		return 0, errors.New("HTTP range offset must not be negative")
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
		return 0, fmt.Errorf("construct HTTP range request: %w", err)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	if s.etag != "" {
		request.Header.Set("If-Range", s.etag)
	} else if s.lastModified != "" {
		request.Header.Set("If-Range", s.lastModified)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("request HTTP range: %w", err)
	}
	defer func() {
		readErr = errors.Join(readErr, response.Body.Close())
	}()
	if response.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP range requires status 206: got %d", response.StatusCode)
	}
	if err := validateContentRange(response.Header.Get("Content-Range"), offset, end, s.size); err != nil {
		return 0, err
	}
	if err := s.validateResponseValidator(response); err != nil {
		return 0, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, wanted+1))
	if err != nil {
		return 0, fmt.Errorf("read HTTP range body: %w", err)
	}
	if int64(len(body)) != wanted {
		return 0, fmt.Errorf("HTTP range body length mismatch: got %d want %d", len(body), wanted)
	}
	count = copy(destination, body)
	if partial {
		return count, io.EOF
	}
	return count, nil
}

// Close prevents future reads from this logical source.
func (s *Source) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *Source) validateResponseValidator(response *http.Response) error {
	if s.etag != "" && response.Header.Get("ETag") != s.etag {
		return errors.New("HTTP range object validator changed")
	}
	if s.etag == "" && s.lastModified != "" && response.Header.Get("Last-Modified") != s.lastModified {
		return errors.New("HTTP range object validator changed")
	}
	return nil
}

func validateObjectID(objectID string) error {
	trimmed := strings.TrimSpace(objectID)
	if trimmed == "" || trimmed == "." || trimmed == ".." || strings.ContainsAny(objectID, "/\\") || strings.ContainsRune(objectID, 0) {
		return fmt.Errorf("invalid HTTP range object ID: %q", objectID)
	}
	return nil
}

func validateContentRange(value string, expectedStart int64, expectedEnd int64, expectedSize int64) error {
	matches := contentRangePattern.FindStringSubmatch(value)
	if len(matches) != 4 {
		return fmt.Errorf("invalid HTTP Content-Range: %q", value)
	}
	values := make([]int64, 3)
	for index, raw := range matches[1:] {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("parse HTTP Content-Range %q: %w", value, err)
		}
		values[index] = parsed
	}
	if values[0] != expectedStart || values[1] != expectedEnd || values[2] != expectedSize {
		return fmt.Errorf(
			"HTTP Content-Range mismatch: got %d-%d/%d want %d-%d/%d",
			values[0], values[1], values[2], expectedStart, expectedEnd, expectedSize,
		)
	}
	return nil
}

var _ acquisition.RangeSource = (*Source)(nil)
