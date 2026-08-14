package acquisition

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const maximumWatchlistExternalIDBytes = 512

// ErrUnsupportedWatchlistMedia means an observed item cannot become an
// acquisition request without additional user policy.
var ErrUnsupportedWatchlistMedia = errors.New("unsupported watchlist media")

// WatchlistMediaType identifies metadata observed in an external watchlist.
type WatchlistMediaType string

const (
	// WatchlistMediaTypeMovie is a movie with complete acquisition intent.
	WatchlistMediaTypeMovie WatchlistMediaType = "movie"
	// WatchlistMediaTypeShow is a show that still needs season/episode policy.
	WatchlistMediaTypeShow WatchlistMediaType = "show"
)

// WatchlistItemInput contains one untrusted external watchlist record.
type WatchlistItemInput struct {
	Source     string
	ExternalID string
	MediaType  WatchlistMediaType
	Title      string
	Year       int
}

// WatchlistItem is validated provider-neutral media intent.
type WatchlistItem struct {
	source     string
	externalID string
	mediaType  WatchlistMediaType
	title      string
	year       int
}

// NewWatchlistItem validates and normalizes an external watchlist record.
func NewWatchlistItem(input WatchlistItemInput) (WatchlistItem, error) {
	if _, err := domain.NewBackingRef(input.Source, "watchlist-item"); err != nil {
		return WatchlistItem{}, fmt.Errorf("invalid watchlist source: %w", err)
	}
	for _, character := range input.ExternalID {
		if unicode.IsControl(character) {
			return WatchlistItem{}, errors.New("watchlist external ID must not contain control characters")
		}
	}
	externalID := strings.TrimSpace(input.ExternalID)
	if externalID == "" {
		return WatchlistItem{}, errors.New("watchlist external ID is required")
	}
	if len(externalID) > maximumWatchlistExternalIDBytes {
		return WatchlistItem{}, fmt.Errorf("watchlist external ID must not exceed %d bytes", maximumWatchlistExternalIDBytes)
	}
	if input.MediaType != WatchlistMediaTypeMovie && input.MediaType != WatchlistMediaTypeShow {
		return WatchlistItem{}, fmt.Errorf("unsupported watchlist media type: %q", input.MediaType)
	}
	title, err := validateSearchText("watchlist title", input.Title, maximumSearchTitleBytes)
	if err != nil {
		return WatchlistItem{}, err
	}
	if err := validateSearchYear(input.Year); err != nil {
		return WatchlistItem{}, err
	}
	return WatchlistItem{
		source: input.Source, externalID: externalID, mediaType: input.MediaType, title: title, year: input.Year,
	}, nil
}

// Source returns the stable watchlist provider name.
func (i WatchlistItem) Source() string { return i.source }

// ExternalID returns the stable provider item identifier.
func (i WatchlistItem) ExternalID() string { return i.externalID }

// MediaType returns the observed provider media type.
func (i WatchlistItem) MediaType() WatchlistMediaType { return i.mediaType }

// Title returns the normalized display title.
func (i WatchlistItem) Title() string { return i.title }

// Year returns the observed release year.
func (i WatchlistItem) Year() int { return i.year }

// SearchRequest maps complete movie metadata into acquisition intent.
func (i WatchlistItem) SearchRequest() (SearchRequest, error) {
	if i.mediaType != WatchlistMediaTypeMovie {
		return SearchRequest{}, ErrUnsupportedWatchlistMedia
	}
	return NewMovieSearch(i.title, i.year)
}
