// Package acquisition orchestrates provider-neutral cached media acquisition.
package acquisition

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

var (
	// ErrNotCached indicates that no ranked torrent release is immediately
	// available from the configured cached-only provider.
	ErrNotCached = errors.New("no cached release is available")
	// ErrNoPlayableMedia indicates that a ready account object contains no video
	// matching the requested movie or episode.
	ErrNoPlayableMedia = errors.New("acquired object contains no matching playable media")
	// ErrUnavailable is a public-safe acquisition boundary failure.
	ErrUnavailable = errors.New("acquisition is temporarily unavailable")
	// ErrAmbiguousMutation means a provider or publication mutation may have
	// committed and must not be retried automatically.
	ErrAmbiguousMutation = errors.New("acquisition mutation outcome is ambiguous")
)

// Searcher returns validated releases in policy rank order.
type Searcher interface {
	Search(ctx context.Context, request acquisitiondomain.SearchRequest) ([]acquisitiondomain.Release, error)
}

// CachedGateway owns provider cache lookup, cached-only creation, and fresh
// inspection. The service owns selection and readiness policy.
type CachedGateway interface {
	CachedTorrents(ctx context.Context, releases []acquisitiondomain.Release) ([]acquisitiondomain.Release, error)
	CreateCachedTorrent(ctx context.Context, release acquisitiondomain.Release) (acquisitiondomain.CreatedObject, error)
	InspectCreatedTorrent(ctx context.Context, created acquisitiondomain.CreatedObject) (acquisitiondomain.PreparationInspection, error)
}

// Publisher atomically exposes one acquired media result to the catalog.
type Publisher interface {
	PublishAcquired(ctx context.Context, media acquisitiondomain.AcquiredMedia) error
}

// Options bounds account-object readiness polling.
type Options struct {
	InspectionAttempts int
	InspectionInterval time.Duration
}

// Service coordinates ranked cached-only acquisition and publication.
type Service struct {
	searcher  Searcher
	gateway   CachedGateway
	publisher Publisher
	options   Options
}

// New constructs a cached acquisition service from narrow boundaries.
func New(searcher Searcher, gateway CachedGateway, publisher Publisher, options Options) (*Service, error) {
	if searcher == nil || gateway == nil || publisher == nil {
		return nil, errors.New("cached acquisition dependencies are required")
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	return &Service{searcher: searcher, gateway: gateway, publisher: publisher, options: options}, nil
}

func validateOptions(options Options) error {
	if options.InspectionAttempts < 1 || options.InspectionAttempts > 100 {
		return errors.New("inspection attempts must be between 1 and 100")
	}
	if options.InspectionInterval <= 0 {
		return errors.New("inspection interval must be positive")
	}
	return nil
}

// Acquire searches, creates exactly one cached account object, waits for
// eligible media, and atomically publishes the selected result.
func (s *Service) Acquire(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error) {
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("acquire media: %w", err)
	}
	validated, err := validateRequest(request)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, err
	}
	releases, err := s.searcher.Search(ctx, validated)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, publicBoundaryError("search acquisition releases", err)
	}
	var preferred acquisitiondomain.Release
	for _, release := range releases {
		if release.Protocol() == acquisitiondomain.ReleaseProtocolTorrent && release.InfoHash() != "" {
			preferred = release
			break
		}
	}
	if preferred.InfoHash() == "" {
		return acquisitiondomain.AcquiredMedia{}, ErrNotCached
	}
	cached, err := s.gateway.CachedTorrents(ctx, []acquisitiondomain.Release{preferred})
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, publicBoundaryError("check acquisition cache", err)
	}
	if len(cached) == 0 || cached[0].Protocol() != preferred.Protocol() || cached[0].InfoHash() != preferred.InfoHash() {
		return acquisitiondomain.AcquiredMedia{}, ErrNotCached
	}
	selectedRelease := cached[0]
	created, err := s.gateway.CreateCachedTorrent(ctx, selectedRelease)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			return acquisitiondomain.AcquiredMedia{}, publicBoundaryError("create cached acquisition object", err)
		}
		return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("create cached acquisition object", err)
	}
	items, err := s.waitForMedia(ctx, created)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("inspect created acquisition object", err)
	}
	selected, err := SelectCandidate(validated, items)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("select created acquisition media", err)
	}
	result, err := acquisitiondomain.NewAcquiredMedia(validated, selectedRelease, selected)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("validate created acquisition media", ErrNoPlayableMedia)
	}
	if err := s.publisher.PublishAcquired(ctx, result); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("publish acquired media", contextErr)
		}
		return acquisitiondomain.AcquiredMedia{}, ambiguousMutationError("publish acquired media", ErrUnavailable)
	}
	return result, nil
}

func validateRequest(request acquisitiondomain.SearchRequest) (acquisitiondomain.SearchRequest, error) {
	switch request.MediaType() {
	case domain.MediaTypeMovie:
		return acquisitiondomain.NewMovieSearch(request.Title(), request.Year())
	case domain.MediaTypeEpisode:
		return acquisitiondomain.NewEpisodeSearch(request.Title(), request.Year(), request.Season(), request.Episode())
	default:
		return acquisitiondomain.SearchRequest{}, errors.New("cached acquisition requires validated movie or episode intent")
	}
}

func publicBoundaryError(action string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", action, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", action, context.DeadlineExceeded)
	}
	if errors.Is(err, domain.ErrUnauthorized) {
		return fmt.Errorf("%s: %w", action, domain.ErrUnauthorized)
	}
	return fmt.Errorf("%s: %w", action, ErrUnavailable)
}

func ambiguousMutationError(action string, err error) error {
	causes := []error{ErrAmbiguousMutation}
	for _, safe := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		domain.ErrUnauthorized,
		ErrNoPlayableMedia,
		ErrUnavailable,
	} {
		if errors.Is(err, safe) {
			causes = append(causes, safe)
		}
	}
	return fmt.Errorf("%s: %w", action, errors.Join(causes...))
}

func (s *Service) waitForMedia(ctx context.Context, created acquisitiondomain.CreatedObject) ([]domain.MediaCandidate, error) {
	for attempt := 0; attempt < s.options.InspectionAttempts; attempt++ {
		inspection, err := s.gateway.InspectCreatedTorrent(ctx, created)
		if err == nil {
			return inspection.Candidates(), nil
		}
		if !errors.Is(err, acquisitiondomain.ErrNotReady) && !errors.Is(err, domain.ErrNotFound) {
			return nil, publicBoundaryError("inspect cached acquisition object", err)
		}
		if attempt == s.options.InspectionAttempts-1 {
			return nil, fmt.Errorf("cached acquisition object did not become ready: %w", ErrUnavailable)
		}
		if err := waitForInspection(ctx, s.options.InspectionInterval); err != nil {
			return nil, fmt.Errorf("wait for cached acquisition object: %w", err)
		}
	}
	return nil, ErrUnavailable
}

func waitForInspection(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SelectCandidate deterministically chooses the playable provider file that
// best matches validated movie or episode intent.
func SelectCandidate(request acquisitiondomain.SearchRequest, candidates []domain.MediaCandidate) (domain.MediaCandidate, error) {
	matching := make([]domain.MediaCandidate, 0, len(candidates))
	if request.MediaType() == domain.MediaTypeEpisode {
		episodeToken := fmt.Sprintf("s%02de%02d", request.Season(), request.Episode())
		for _, candidate := range candidates {
			if containsComparisonPhrase(candidate.Name, episodeToken) {
				matching = append(matching, candidate)
			}
		}
		if len(matching) == 0 {
			return domain.MediaCandidate{}, ErrNoPlayableMedia
		}
	} else {
		for _, candidate := range candidates {
			if containsComparisonPhrase(candidate.Name, request.Title()) {
				matching = append(matching, candidate)
			}
		}
		if len(matching) == 0 {
			matching = append(matching, candidates...)
		}
	}
	if len(matching) == 0 {
		return domain.MediaCandidate{}, ErrNoPlayableMedia
	}
	sort.Slice(matching, func(left int, right int) bool {
		if matching[left].Size != matching[right].Size {
			return matching[left].Size > matching[right].Size
		}
		leftName := strings.ToLower(matching[left].Name)
		rightName := strings.ToLower(matching[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return matching[left].ObjectID < matching[right].ObjectID
	})
	return matching[0], nil
}

func containsComparisonPhrase(value string, phrase string) bool {
	haystack := " " + normalizeComparisonText(value) + " "
	needle := " " + normalizeComparisonText(phrase) + " "
	return needle != "  " && strings.Contains(haystack, needle)
}

func normalizeComparisonText(value string) string {
	words := strings.Fields(strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			return unicode.ToLower(character)
		}
		return ' '
	}, value))
	return strings.Join(words, " ")
}
