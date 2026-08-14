package acquisition

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	maximumSearchTitleBytes = 200
	maximumSourceIDBytes    = 512
	maximumIndexerNameBytes = 200
)

// SearchRequest is validated provider-neutral movie or episode intent.
type SearchRequest struct {
	mediaType domain.MediaType
	title     string
	year      int
	season    int
	episode   int
}

// NewMovieSearch constructs validated movie search intent.
func NewMovieSearch(title string, year int) (SearchRequest, error) {
	cleanTitle, err := validateSearchText("movie title", title, maximumSearchTitleBytes)
	if err != nil {
		return SearchRequest{}, err
	}
	if err := validateSearchYear(year); err != nil {
		return SearchRequest{}, err
	}
	return SearchRequest{mediaType: domain.MediaTypeMovie, title: cleanTitle, year: year}, nil
}

// NewEpisodeSearch constructs validated TV episode search intent.
func NewEpisodeSearch(showTitle string, year int, season int, episode int) (SearchRequest, error) {
	cleanTitle, err := validateSearchText("show title", showTitle, maximumSearchTitleBytes)
	if err != nil {
		return SearchRequest{}, err
	}
	if err := validateSearchYear(year); err != nil {
		return SearchRequest{}, err
	}
	if season < 0 || season > 99 {
		return SearchRequest{}, fmt.Errorf("season must be between 0 and 99: %d", season)
	}
	if episode < 1 || episode > 999 {
		return SearchRequest{}, fmt.Errorf("episode must be between 1 and 999: %d", episode)
	}
	return SearchRequest{
		mediaType: domain.MediaTypeEpisode,
		title:     cleanTitle,
		year:      year,
		season:    season,
		episode:   episode,
	}, nil
}

// MediaType returns the requested Plex media hierarchy.
func (r SearchRequest) MediaType() domain.MediaType { return r.mediaType }

// Title returns the normalized movie or show title.
func (r SearchRequest) Title() string { return r.title }

// Year returns the requested release year.
func (r SearchRequest) Year() int { return r.year }

// Season returns the requested season, or zero for movies.
func (r SearchRequest) Season() int { return r.season }

// Episode returns the requested episode, or zero for movies.
func (r SearchRequest) Episode() int { return r.episode }

// Query returns the literal text sent to an authorized search provider.
func (r SearchRequest) Query() string {
	if r.mediaType == domain.MediaTypeEpisode {
		return fmt.Sprintf("%s S%02dE%02d", r.title, r.season, r.episode)
	}
	return fmt.Sprintf("%s %d", r.title, r.year)
}

func validateSearchYear(year int) error {
	if year < 1888 || year > 2100 {
		return fmt.Errorf("year must be between 1888 and 2100: %d", year)
	}
	return nil
}

func validateSearchText(name string, value string, maximumBytes int) (string, error) {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(clean) > maximumBytes {
		return "", fmt.Errorf("%s must not exceed %d bytes", name, maximumBytes)
	}
	for _, character := range clean {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return clean, nil
}

// ReleaseProtocol identifies how an authorized search result can be acquired.
type ReleaseProtocol string

const (
	// ReleaseProtocolTorrent is a BitTorrent release.
	ReleaseProtocolTorrent ReleaseProtocol = "torrent"
	// ReleaseProtocolUsenet is a Usenet/NZB release.
	ReleaseProtocolUsenet ReleaseProtocol = "usenet"
)

// ProviderCapabilities describes the locators an acquisition search provider can return.
type ProviderCapabilities struct {
	protocols    []ReleaseProtocol
	infoHashes   bool
	magnetURLs   bool
	downloadURLs bool
}

// NewProviderCapabilities constructs an immutable capability snapshot.
func NewProviderCapabilities(protocols []ReleaseProtocol, infoHashes bool, magnetURLs bool, downloadURLs bool) ProviderCapabilities {
	return ProviderCapabilities{
		protocols: append([]ReleaseProtocol(nil), protocols...), infoHashes: infoHashes,
		magnetURLs: magnetURLs, downloadURLs: downloadURLs,
	}
}

// Protocols returns an independent copy of supported protocols.
func (c ProviderCapabilities) Protocols() []ReleaseProtocol {
	return append([]ReleaseProtocol(nil), c.protocols...)
}

// InfoHashes reports whether results can include BitTorrent hashes.
func (c ProviderCapabilities) InfoHashes() bool { return c.infoHashes }

// MagnetURLs reports whether results can include magnet locators.
func (c ProviderCapabilities) MagnetURLs() bool { return c.magnetURLs }

// DownloadURLs reports whether results can include HTTP(S) release locators.
func (c ProviderCapabilities) DownloadURLs() bool { return c.downloadURLs }

// ReleaseInput contains one untrusted provider result for validation.
type ReleaseInput struct {
	Provider    string
	SourceID    string
	Title       string
	Protocol    ReleaseProtocol
	Size        int64
	Indexer     string
	InfoHash    string
	MagnetURL   string
	DownloadURL string
	Seeders     *int
}

// Release is a validated ephemeral acquisition candidate.
type Release struct {
	provider    string
	sourceID    string
	title       string
	protocol    ReleaseProtocol
	size        int64
	indexer     string
	infoHash    string
	magnetURL   string
	downloadURL string
	seeders     int
	hasSeeders  bool
}

// NewRelease validates and normalizes one provider release.
func NewRelease(input ReleaseInput) (Release, error) {
	if _, err := domain.NewBackingRef(input.Provider, "release"); err != nil {
		return Release{}, fmt.Errorf("invalid release provider: %w", err)
	}
	sourceID, err := validateSearchText("release source ID", input.SourceID, maximumSourceIDBytes)
	if err != nil {
		return Release{}, err
	}
	title, err := validateSearchText("release title", input.Title, maximumSearchTitleBytes)
	if err != nil {
		return Release{}, err
	}
	indexer, err := validateSearchText("release indexer", input.Indexer, maximumIndexerNameBytes)
	if err != nil {
		return Release{}, err
	}
	if input.Protocol != ReleaseProtocolTorrent && input.Protocol != ReleaseProtocolUsenet {
		return Release{}, fmt.Errorf("unsupported release protocol: %q", input.Protocol)
	}
	if input.Size <= 0 {
		return Release{}, fmt.Errorf("release size must be positive: %d", input.Size)
	}
	infoHash, err := normalizeInfoHash(input.InfoHash)
	if err != nil {
		return Release{}, err
	}
	if input.MagnetURL != "" {
		if err := validateMagnetURL(input.MagnetURL); err != nil {
			return Release{}, err
		}
	}
	if input.DownloadURL != "" {
		if err := validateReleaseDownloadURL(input.DownloadURL); err != nil {
			return Release{}, err
		}
	}
	if input.Protocol == ReleaseProtocolTorrent && infoHash == "" && input.MagnetURL == "" && input.DownloadURL == "" {
		return Release{}, errors.New("torrent release requires an info hash, magnet URL, or download URL")
	}
	if input.Protocol == ReleaseProtocolUsenet && input.DownloadURL == "" {
		return Release{}, errors.New("usenet release requires a download URL")
	}
	seeders := 0
	hasSeeders := input.Seeders != nil
	if hasSeeders {
		if *input.Seeders < 0 {
			return Release{}, errors.New("release seeders must not be negative")
		}
		seeders = *input.Seeders
	}
	return Release{
		provider: input.Provider, sourceID: sourceID, title: title, protocol: input.Protocol, size: input.Size, indexer: indexer,
		infoHash: infoHash, magnetURL: input.MagnetURL, downloadURL: input.DownloadURL,
		seeders: seeders, hasSeeders: hasSeeders,
	}, nil
}

func normalizeInfoHash(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) == 40 {
		if _, err := hex.DecodeString(value); err != nil {
			return "", errors.New("release info hash must be hexadecimal SHA-1 or base32 BTIH")
		}
		return strings.ToLower(value), nil
	}
	if len(value) == 32 {
		if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value)); err != nil {
			return "", errors.New("release info hash must be hexadecimal SHA-1 or base32 BTIH")
		}
		return strings.ToUpper(value), nil
	}
	return "", errors.New("release info hash must be hexadecimal SHA-1 or base32 BTIH")
}

func validateMagnetURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "magnet" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("release magnet URL is invalid")
	}
	for _, exactTopic := range parsed.Query()["xt"] {
		const prefix = "urn:btih:"
		if len(exactTopic) > len(prefix) && strings.EqualFold(exactTopic[:len(prefix)], prefix) {
			if _, hashErr := normalizeInfoHash(exactTopic[len(prefix):]); hashErr == nil {
				return nil
			}
		}
	}
	return errors.New("release magnet URL requires a valid BitTorrent info hash")
}

func validateReleaseDownloadURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("release download URL must be absolute HTTP(S) without credentials or fragment")
	}
	return nil
}

// Provider returns the stable search-provider name.
func (r Release) Provider() string { return r.provider }

// SourceID returns the provider-local stable result identifier.
func (r Release) SourceID() string { return r.sourceID }

// Title returns the normalized release title.
func (r Release) Title() string { return r.title }

// Protocol returns the acquisition protocol.
func (r Release) Protocol() ReleaseProtocol { return r.protocol }

// Size returns the advertised logical release size.
func (r Release) Size() int64 { return r.size }

// Indexer returns the provider-reported indexer name.
func (r Release) Indexer() string { return r.indexer }

// InfoHash returns the normalized BitTorrent hash, when present.
func (r Release) InfoHash() string { return r.infoHash }

// MagnetURL returns the ephemeral magnet locator, when present.
func (r Release) MagnetURL() string { return r.magnetURL }

// DownloadURL returns the ephemeral HTTP(S) release locator, when present.
func (r Release) DownloadURL() string { return r.downloadURL }

// Seeders returns the provider-reported torrent seed count.
func (r Release) Seeders() int { return r.seeders }

// HasSeeders reports whether the provider supplied a seed count.
func (r Release) HasSeeders() bool { return r.hasSeeders }
