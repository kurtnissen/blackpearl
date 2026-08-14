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

// WatchlistShowPolicy controls whether a show-level observation may become an
// exact episode intent.
type WatchlistShowPolicy string

const (
	// WatchlistShowPolicyOff keeps shows observation-only.
	WatchlistShowPolicyOff WatchlistShowPolicy = "off"
	// WatchlistShowPolicyPilot maps a newly eligible show to S01E01.
	WatchlistShowPolicyPilot WatchlistShowPolicy = "pilot"
)

// WatchlistPolicy is the durable automatic-acquisition policy.
type WatchlistPolicy struct {
	acquisitionEnabled bool
	showPolicy         WatchlistShowPolicy
}

// NewWatchlistPolicy validates one durable Watchlist policy.
func NewWatchlistPolicy(acquisitionEnabled bool, showPolicy WatchlistShowPolicy) (WatchlistPolicy, error) {
	if showPolicy != WatchlistShowPolicyOff && showPolicy != WatchlistShowPolicyPilot {
		return WatchlistPolicy{}, fmt.Errorf("unsupported Watchlist show policy: %q", showPolicy)
	}
	return WatchlistPolicy{acquisitionEnabled: acquisitionEnabled, showPolicy: showPolicy}, nil
}

// AcquisitionEnabled reports whether new automatic acquisition is enabled.
func (p WatchlistPolicy) AcquisitionEnabled() bool { return p.acquisitionEnabled }

// ShowPolicy returns the explicit show-level intent policy.
func (p WatchlistPolicy) ShowPolicy() WatchlistShowPolicy { return p.showPolicy }

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

// WatchlistObservation couples one provider item to immutable queue intent.
type WatchlistObservation struct {
	item         WatchlistItem
	autoEligible bool
	season       int
	episode      int
}

// NewWatchlistObservation validates one provider observation and its exact
// immutable acquisition coordinates.
func NewWatchlistObservation(
	item WatchlistItem,
	autoEligible bool,
	season int,
	episode int,
) (WatchlistObservation, error) {
	validated, err := NewWatchlistItem(WatchlistItemInput{
		Source: item.Source(), ExternalID: item.ExternalID(), MediaType: item.MediaType(),
		Title: item.Title(), Year: item.Year(),
	})
	if err != nil {
		return WatchlistObservation{}, fmt.Errorf("invalid Watchlist observation item: %w", err)
	}
	switch validated.MediaType() {
	case WatchlistMediaTypeMovie:
		if season != 0 || episode != 0 {
			return WatchlistObservation{}, errors.New("movie observation must not contain episode coordinates")
		}
	case WatchlistMediaTypeShow:
		if !autoEligible {
			if season != 0 || episode != 0 {
				return WatchlistObservation{}, errors.New("observation-only show must not contain episode coordinates")
			}
			break
		}
		if season < 1 || season > 99 {
			return WatchlistObservation{}, fmt.Errorf("eligible show season must be between 1 and 99: %d", season)
		}
		if episode < 1 || episode > 999 {
			return WatchlistObservation{}, fmt.Errorf("eligible show episode must be between 1 and 999: %d", episode)
		}
	default:
		return WatchlistObservation{}, errors.New("unsupported Watchlist observation media type")
	}
	return WatchlistObservation{
		item: validated, autoEligible: autoEligible, season: season, episode: episode,
	}, nil
}

// Item returns the validated provider observation.
func (o WatchlistObservation) Item() WatchlistItem { return o.item }

// AutoEligible reports whether the first observation created acquisition intent.
func (o WatchlistObservation) AutoEligible() bool { return o.autoEligible }

// Season returns the immutable episode season, or zero when not applicable.
func (o WatchlistObservation) Season() int { return o.season }

// Episode returns the immutable episode number, or zero when not applicable.
func (o WatchlistObservation) Episode() int { return o.episode }

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
