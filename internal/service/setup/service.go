// Package setup orchestrates browser-driven provider discovery and runtime activation.
package setup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

const setupSessionPurpose = "blackpearl-local-setup-session-v1"

var (
	// ErrSetupUnauthorized indicates that the local browser has not proven it
	// received the host-generated setup pairing value or a saved-token session.
	ErrSetupUnauthorized = errors.New("setup browser is not paired")
	// ErrUnauthorized is a public-safe provider authentication failure.
	ErrUnauthorized = errors.New("provider credentials were rejected")
	// ErrUnavailable is a public-safe provider or runtime availability failure.
	ErrUnavailable = errors.New("provider is temporarily unavailable")
	// ErrDegraded indicates that a reachable saved subset is active while
	// BlackPearl continues retrying the complete saved manifest.
	ErrDegraded = errors.New("saved setup is partially available")
	// ErrInvalidSelection indicates that the requested object is not eligible.
	ErrInvalidSelection = errors.New("selected media is not available")
)

// Repository stores one token and one selected manifest.
type Repository interface {
	LoadManifest(ctx context.Context) (string, domain.SetupManifest, error)
	SaveManifest(ctx context.Context, token string, manifest domain.SetupManifest) error
	Clear(ctx context.Context) error
}

// Discoverer lists eligible account media without requesting content bytes.
type Discoverer interface {
	Discover(ctx context.Context) ([]domain.MediaCandidate, error)
}

// GatewayFactory constructs an isolated discovery gateway for one token.
type GatewayFactory func(token string) (Discoverer, error)

// RuntimeFactory prepares and validates a range-backed catalog without activating it.
type RuntimeFactory func(ctx context.Context, token string, manifest domain.SetupManifest) (core.CatalogService, error)

// RestorePreparation contains one immutable startup catalog and the exact
// saved-manifest subset it exposes.
type RestorePreparation struct {
	Runtime        core.CatalogService
	ActiveManifest domain.SetupManifest
}

// RestoreRuntimeFactory prepares a complete or transiently reduced saved
// manifest without activating or persisting it.
type RestoreRuntimeFactory func(ctx context.Context, token string, manifest domain.SetupManifest) (RestorePreparation, error)

// Publisher atomically publishes one namespace-and-catalog runtime snapshot.
type Publisher interface {
	Publish(ctx context.Context, next core.CatalogService) error
}

// ApplyRequest contains write-only credentials and public Plex metadata.
type ApplyRequest struct {
	Token    string             `json:"token,omitempty"`
	Items    []ApplyItemRequest `json:"items,omitempty"`
	ObjectID string             `json:"objectId,omitempty"`
	Title    string             `json:"title,omitempty"`
	Year     int                `json:"year,omitempty"`
}

// ApplyItemRequest identifies one discovered item and its Plex metadata.
type ApplyItemRequest struct {
	ObjectID  string           `json:"objectId"`
	MediaType domain.MediaType `json:"mediaType,omitempty"`
	Title     string           `json:"title"`
	Year      int              `json:"year"`
	ShowTitle string           `json:"showTitle,omitempty"`
	Season    int              `json:"season,omitempty"`
	Episode   int              `json:"episode,omitempty"`
}

// Status is safe to return to an untrusted browser.
type Status struct {
	SetupRequired        bool                        `json:"setupRequired"`
	TokenConfigured      bool                        `json:"tokenConfigured"`
	Selected             *domain.SetupConfiguration  `json:"selected,omitempty"`
	SelectedItems        []domain.SetupConfiguration `json:"selectedItems,omitempty"`
	SavedItemCount       int                         `json:"savedItemCount"`
	ActiveItemCount      int                         `json:"activeItemCount"`
	UnavailableItemCount int                         `json:"unavailableItemCount"`
	Degraded             bool                        `json:"degraded"`
}

// Service owns setup state transitions.
type Service struct {
	repository     Repository
	gatewayFactory GatewayFactory
	runtimeFactory RuntimeFactory
	restoreFactory RestoreRuntimeFactory
	publisher      Publisher
	bootstrapToken string

	mu              sync.RWMutex
	transitionMu    sync.Mutex
	tokenConfigured bool
	manifest        *domain.SetupManifest
	savedItemCount  int
}

// New constructs a setup service from narrow infrastructure boundaries.
func New(repository Repository, gatewayFactory GatewayFactory, runtimeFactory RuntimeFactory, publisher Publisher, bootstrapToken ...string) *Service {
	restoreFactory := func(ctx context.Context, token string, manifest domain.SetupManifest) (RestorePreparation, error) {
		runtime, err := runtimeFactory(ctx, token, manifest)
		return RestorePreparation{Runtime: runtime, ActiveManifest: manifest}, err
	}
	return NewWithRestore(repository, gatewayFactory, runtimeFactory, restoreFactory, publisher, bootstrapToken...)
}

// NewWithRestore constructs a setup service with a restore-only degraded
// preparation boundary while preserving full transactional runtime creation.
func NewWithRestore(
	repository Repository,
	gatewayFactory GatewayFactory,
	runtimeFactory RuntimeFactory,
	restoreFactory RestoreRuntimeFactory,
	publisher Publisher,
	bootstrapToken ...string,
) *Service {
	service := &Service{
		repository: repository, gatewayFactory: gatewayFactory, runtimeFactory: runtimeFactory,
		restoreFactory: restoreFactory, publisher: publisher,
	}
	if len(bootstrapToken) > 0 {
		service.bootstrapToken = bootstrapToken[0]
	}
	return service
}

// Status reports only non-secret setup state.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := Status{SetupRequired: s.manifest == nil, TokenConfigured: s.tokenConfigured}
	status.SavedItemCount = s.savedItemCount
	if s.manifest != nil {
		status.SelectedItems = append([]domain.SetupConfiguration(nil), s.manifest.Items...)
		status.ActiveItemCount = len(status.SelectedItems)
		if len(status.SelectedItems) > 0 {
			copy := status.SelectedItems[0]
			status.Selected = &copy
		}
	}
	status.UnavailableItemCount = max(0, status.SavedItemCount-status.ActiveItemCount)
	status.Degraded = status.ActiveItemCount > 0 && status.UnavailableItemCount > 0
	return status
}

// Discover lists eligible media using a new token or the securely saved token.
func (s *Service) Discover(ctx context.Context, token string) ([]domain.MediaCandidate, error) {
	resolved, err := s.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}
	gateway, err := s.gatewayFactory(resolved)
	if err != nil {
		return nil, fmt.Errorf("configure provider discovery: %w", ErrUnavailable)
	}
	items, err := gateway.Discover(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			return nil, fmt.Errorf("discover provider media: %w", ErrUnauthorized)
		}
		return nil, fmt.Errorf("discover provider media: %w", ErrUnavailable)
	}
	return items, nil
}

// AuthorizeSetup permits first-time setup, a browser session derived from the
// securely saved token, or explicit re-entry of that saved token.
func (s *Service) AuthorizeSetup(ctx context.Context, suppliedToken string, session string, bootstrapToken string) error {
	savedToken, _, err := s.repository.LoadManifest(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			if suppliedToken == "" || !s.validBootstrap(bootstrapToken) {
				return ErrSetupUnauthorized
			}
			return nil
		}
		return fmt.Errorf("load setup authorization: %w", ErrUnavailable)
	}
	s.markTokenConfigured()
	if s.validBootstrap(bootstrapToken) {
		return nil
	}
	expected, err := sessionForToken(savedToken)
	if err != nil {
		return fmt.Errorf("derive setup authorization: %w", ErrUnavailable)
	}
	if len(session) == len(expected) && subtle.ConstantTimeCompare([]byte(session), []byte(expected)) == 1 {
		return nil
	}
	if suppliedToken != "" && len(suppliedToken) == len(savedToken) && subtle.ConstantTimeCompare([]byte(suppliedToken), []byte(savedToken)) == 1 {
		return nil
	}
	return ErrSetupUnauthorized
}

func (s *Service) validBootstrap(provided string) bool {
	return len(provided) == len(s.bootstrapToken) && len(provided) != 0 && subtle.ConstantTimeCompare([]byte(provided), []byte(s.bootstrapToken)) == 1
}

// IssueSession returns an opaque browser bearer derived from the active token.
func (s *Service) IssueSession(ctx context.Context, token string) (string, error) {
	resolved, err := s.resolveToken(ctx, token)
	if err != nil {
		return "", err
	}
	value, err := sessionForToken(resolved)
	if err != nil {
		return "", fmt.Errorf("derive setup session: %w", ErrUnavailable)
	}
	return value, nil
}

// Apply validates and probes one selected manifest, persists its credentials
// and metadata, then atomically publishes the prepared runtime.
func (s *Service) Apply(ctx context.Context, request ApplyRequest) (domain.SetupManifest, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	token, err := s.resolveToken(ctx, request.Token)
	if err != nil {
		return domain.SetupManifest{}, err
	}
	requests, err := request.itemRequests()
	if err != nil {
		return domain.SetupManifest{}, ErrInvalidSelection
	}
	items, err := s.discoverWithToken(ctx, token)
	if err != nil {
		return domain.SetupManifest{}, err
	}
	candidates := make(map[string]domain.MediaCandidate, len(items))
	for index := range items {
		candidates[items[index].ObjectID] = items[index]
	}
	configurations := make([]domain.SetupConfiguration, 0, len(requests))
	for index := range requests {
		selected, exists := candidates[requests[index].ObjectID]
		if !exists {
			return domain.SetupManifest{}, ErrInvalidSelection
		}
		configuration, configurationErr := configurationFromRequest(selected, requests[index])
		if configurationErr != nil {
			return domain.SetupManifest{}, fmt.Errorf("validate selected media: %w", ErrInvalidSelection)
		}
		configurations = append(configurations, configuration)
	}
	manifest, err := domain.NewSetupManifest(configurations)
	if err != nil {
		return domain.SetupManifest{}, fmt.Errorf("validate selected manifest: %w", ErrInvalidSelection)
	}
	return s.commitManifest(ctx, token, manifest)
}

// PublishAcquired appends or replaces one validated acquired item using the
// same durable, atomic runtime transaction as manual setup.
func (s *Service) PublishAcquired(ctx context.Context, media acquisitiondomain.AcquiredMedia) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	token, current, err := s.repository.LoadManifest(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrUnauthorized
		}
		return fmt.Errorf("load setup before acquired publication: %w", ErrUnavailable)
	}
	configuration, err := configurationFromAcquired(media)
	if err != nil {
		return fmt.Errorf("validate acquired media: %w", ErrInvalidSelection)
	}
	items := append([]domain.SetupConfiguration(nil), current.Items...)
	replaced := false
	for index := range items {
		if items[index].Backing() == configuration.Backing() || sameLogicalMedia(items[index], configuration) {
			items[index] = configuration
			replaced = true
			break
		}
	}
	if !replaced {
		if len(items) == domain.MaximumSetupManifestItems {
			return fmt.Errorf("setup manifest is at capacity: %w", ErrInvalidSelection)
		}
		items = append(items, configuration)
	}
	manifest, err := domain.NewSetupManifest(items)
	if err != nil {
		return fmt.Errorf("validate acquired manifest: %w", ErrInvalidSelection)
	}
	_, err = s.commitManifest(ctx, token, manifest)
	return err
}

// FindPublished reports whether exact media intent is already present in the
// durable Plex manifest, without exposing the saved provider credential.
func (s *Service) FindPublished(ctx context.Context, request acquisitiondomain.SearchRequest) (string, bool, error) {
	_, manifest, err := s.repository.LoadManifest(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("load published media manifest: %w", ErrUnavailable)
	}
	for index := range manifest.Items {
		item := manifest.Items[index]
		if publishedMediaMatches(item, request) {
			return item.ObjectID, true, nil
		}
	}
	return "", false, nil
}

// FindPublishedEpisode returns one exact episode from the active manifest by
// its canonical Plex-relative path, without loading a saved credential.
func (s *Service) FindPublishedEpisode(ctx context.Context, virtualPath string) (domain.SetupConfiguration, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.SetupConfiguration{}, false, fmt.Errorf("find published episode: %w", err)
	}
	if path.IsAbs(virtualPath) || path.Clean(virtualPath) != virtualPath || strings.ContainsAny(virtualPath, "\\\x00") {
		return domain.SetupConfiguration{}, false, errors.New("published episode path is invalid")
	}
	if !strings.HasPrefix(virtualPath, "TV Shows/") {
		return domain.SetupConfiguration{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.manifest == nil {
		return domain.SetupConfiguration{}, false, nil
	}
	for _, item := range s.manifest.Items {
		if item.MediaType != domain.MediaTypeEpisode {
			continue
		}
		itemPath, err := item.VirtualPath()
		if err != nil {
			return domain.SetupConfiguration{}, false, fmt.Errorf("derive published episode path: %w", ErrUnavailable)
		}
		if itemPath == virtualPath {
			return item, true, nil
		}
	}
	return domain.SetupConfiguration{}, false, nil
}

// FindPublishedMovie is retained for callers that have not yet adopted exact
// media intent.
func (s *Service) FindPublishedMovie(ctx context.Context, title string, year int) (string, bool, error) {
	request, err := acquisitiondomain.NewMovieSearch(title, year)
	if err != nil {
		return "", false, err
	}
	return s.FindPublished(ctx, request)
}

func publishedMediaMatches(item domain.SetupConfiguration, request acquisitiondomain.SearchRequest) bool {
	if item.MediaType != request.MediaType() || item.Year != request.Year() {
		return false
	}
	switch request.MediaType() {
	case domain.MediaTypeMovie:
		return item.Title == request.Title()
	case domain.MediaTypeEpisode:
		return item.ShowTitle == request.Title() && item.Season == request.Season() && item.Episode == request.Episode()
	default:
		return false
	}
}

func (s *Service) commitManifest(ctx context.Context, token string, manifest domain.SetupManifest) (domain.SetupManifest, error) {
	runtime, err := s.runtimeFactory(ctx, token, manifest)
	if err != nil {
		return domain.SetupManifest{}, errors.Join(fmt.Errorf("prepare selected media: %w", ErrUnavailable), err)
	}
	if err := runtime.Ready(ctx); err != nil {
		return domain.SetupManifest{}, errors.Join(fmt.Errorf("probe selected media: %w", ErrUnavailable), err)
	}
	previousToken, previousManifest, loadErr := s.repository.LoadManifest(ctx)
	hadPrevious := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, domain.ErrNotFound) {
		return domain.SetupManifest{}, fmt.Errorf("load prior setup before commit: %w", ErrUnavailable)
	}
	if saveErr := s.repository.SaveManifest(ctx, token, manifest); saveErr != nil {
		committedToken, committedManifest, loadAfterSaveErr := s.repository.LoadManifest(ctx)
		committed := loadAfterSaveErr == nil && committedToken == token && manifestsEqual(committedManifest, manifest)
		if errors.Is(saveErr, domain.ErrCleanupDeferred) && committed {
			// The new generation is durable and visible; only removal of an
			// inactive private generation was deferred.
		} else if committed {
			rollbackErr := s.rollbackPersistence(ctx, hadPrevious, previousToken, previousManifest)
			return domain.SetupManifest{}, errors.Join(fmt.Errorf("persist selected media: %w", ErrUnavailable), rollbackErr)
		} else {
			return domain.SetupManifest{}, fmt.Errorf("persist selected media: %w", ErrUnavailable)
		}
	}
	if err := s.publisher.Publish(ctx, runtime); err != nil {
		rollbackErr := s.rollbackPersistence(ctx, hadPrevious, previousToken, previousManifest)
		return domain.SetupManifest{}, errors.Join(fmt.Errorf("publish selected media: %w", ErrUnavailable), rollbackErr)
	}
	s.setStatus(manifest, len(manifest.Items))
	return manifest, nil
}

// Restore activates securely persisted setup state during process startup.
func (s *Service) Restore(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	token, manifest, err := s.repository.LoadManifest(ctx)
	if err != nil {
		return err
	}
	s.markSavedManifest(len(manifest.Items))
	preparation, err := s.restoreFactory(ctx, token, manifest)
	if err != nil {
		if errors.Is(err, domain.ErrUnavailable) {
			return errors.Join(fmt.Errorf("prepare saved setup: %w", ErrUnavailable), err)
		}
		return fmt.Errorf("prepare saved setup: %w", err)
	}
	if err := validateRestorePreparation(manifest, preparation); err != nil {
		return fmt.Errorf("validate saved setup preparation: %w", err)
	}
	degraded := len(preparation.ActiveManifest.Items) < len(manifest.Items)
	if degraded && s.hasActiveManifest() {
		return errors.Join(ErrDegraded, ErrUnavailable)
	}
	if err := preparation.Runtime.Ready(ctx); err != nil {
		return fmt.Errorf("probe saved setup: %w", ErrUnavailable)
	}
	if err := s.publisher.Publish(ctx, preparation.Runtime); err != nil {
		return fmt.Errorf("publish saved setup: %w", ErrUnavailable)
	}
	s.setStatus(preparation.ActiveManifest, len(manifest.Items))
	if degraded {
		return errors.Join(ErrDegraded, ErrUnavailable)
	}
	return nil
}

func validateRestorePreparation(saved domain.SetupManifest, preparation RestorePreparation) error {
	if preparation.Runtime == nil {
		return errors.New("restore runtime is required")
	}
	if len(preparation.ActiveManifest.Items) == 0 {
		return errors.New("restore manifest must contain at least one active item")
	}
	if len(preparation.ActiveManifest.Items) > len(saved.Items) {
		return errors.New("restore manifest exceeds saved manifest")
	}
	savedIndex := 0
	for _, active := range preparation.ActiveManifest.Items {
		for savedIndex < len(saved.Items) && saved.Items[savedIndex] != active {
			savedIndex++
		}
		if savedIndex == len(saved.Items) {
			return errors.New("restore manifest is not an ordered saved subset")
		}
		savedIndex++
	}
	return nil
}

func (s *Service) hasActiveManifest() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest != nil
}

func (s *Service) discoverWithToken(ctx context.Context, token string) ([]domain.MediaCandidate, error) {
	gateway, err := s.gatewayFactory(token)
	if err != nil {
		return nil, fmt.Errorf("configure provider discovery: %w", ErrUnavailable)
	}
	items, err := gateway.Discover(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			return nil, fmt.Errorf("discover provider media: %w", ErrUnauthorized)
		}
		return nil, fmt.Errorf("discover provider media: %w", ErrUnavailable)
	}
	return items, nil
}

func (s *Service) resolveToken(ctx context.Context, supplied string) (string, error) {
	if supplied != "" {
		if len(supplied) > 4096 || strings.TrimSpace(supplied) != supplied || strings.ContainsRune(supplied, 0) {
			return "", ErrUnauthorized
		}
		return supplied, nil
	}
	token, _, err := s.repository.LoadManifest(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", ErrUnauthorized
		}
		return "", fmt.Errorf("load saved provider credentials: %w", ErrUnavailable)
	}
	s.markTokenConfigured()
	return token, nil
}

func (s *Service) rollbackPersistence(ctx context.Context, hadPrevious bool, token string, manifest domain.SetupManifest) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if hadPrevious {
		if err := s.repository.SaveManifest(rollbackContext, token, manifest); err != nil {
			return fmt.Errorf("restore prior setup persistence: %w", err)
		}
		return nil
	}
	if err := s.repository.Clear(rollbackContext); err != nil {
		return fmt.Errorf("clear failed setup persistence: %w", err)
	}
	return nil
}

func (s *Service) setStatus(manifest domain.SetupManifest, savedItemCount int) {
	s.mu.Lock()
	copy := domain.SetupManifest{Items: append([]domain.SetupConfiguration(nil), manifest.Items...)}
	s.manifest = &copy
	s.tokenConfigured = true
	s.savedItemCount = savedItemCount
	s.mu.Unlock()
}

func (r ApplyRequest) itemRequests() ([]ApplyItemRequest, error) {
	if len(r.Items) > 0 {
		if len(r.Items) > domain.MaximumSetupManifestItems {
			return nil, errors.New("request contains too many items")
		}
		if r.ObjectID != "" || r.Title != "" || r.Year != 0 {
			return nil, errors.New("request cannot combine items and legacy selection")
		}
		return append([]ApplyItemRequest(nil), r.Items...), nil
	}
	if r.ObjectID == "" {
		return nil, errors.New("request requires at least one item")
	}
	return []ApplyItemRequest{{ObjectID: r.ObjectID, Title: r.Title, Year: r.Year}}, nil
}

func manifestsEqual(left domain.SetupManifest, right domain.SetupManifest) bool {
	if len(left.Items) != len(right.Items) {
		return false
	}
	for index := range left.Items {
		if left.Items[index] != right.Items[index] {
			return false
		}
	}
	return true
}

func configurationFromRequest(candidate domain.MediaCandidate, request ApplyItemRequest) (domain.SetupConfiguration, error) {
	switch request.MediaType {
	case "", domain.MediaTypeMovie:
		return domain.NewSetupConfiguration(candidate, request.Title, request.Year)
	case domain.MediaTypeEpisode:
		return domain.NewSetupEpisodeConfiguration(
			candidate, request.ShowTitle, request.Year, request.Season, request.Episode, request.Title,
		)
	default:
		return domain.SetupConfiguration{}, fmt.Errorf("unsupported media type: %q", request.MediaType)
	}
}

func configurationFromAcquired(media acquisitiondomain.AcquiredMedia) (domain.SetupConfiguration, error) {
	request := media.Request()
	switch request.MediaType() {
	case domain.MediaTypeMovie:
		return domain.NewSetupConfiguration(media.Candidate(), request.Title(), request.Year())
	case domain.MediaTypeEpisode:
		return domain.NewSetupEpisodeConfiguration(
			media.Candidate(), request.Title(), request.Year(), request.Season(), request.Episode(),
			fmt.Sprintf("Episode %d", request.Episode()),
		)
	default:
		return domain.SetupConfiguration{}, fmt.Errorf("unsupported acquired media type: %q", request.MediaType())
	}
}

func sameLogicalMedia(left domain.SetupConfiguration, right domain.SetupConfiguration) bool {
	if left.MediaType != right.MediaType || left.Year != right.Year {
		return false
	}
	switch left.MediaType {
	case domain.MediaTypeMovie:
		return left.Title == right.Title
	case domain.MediaTypeEpisode:
		return left.ShowTitle == right.ShowTitle && left.Season == right.Season && left.Episode == right.Episode
	default:
		return false
	}
}

func (s *Service) markTokenConfigured() {
	s.mu.Lock()
	s.tokenConfigured = true
	s.mu.Unlock()
}

func (s *Service) markSavedManifest(itemCount int) {
	s.mu.Lock()
	s.tokenConfigured = true
	s.savedItemCount = itemCount
	s.mu.Unlock()
}

func sessionForToken(token string) (string, error) {
	digest := hmac.New(sha256.New, []byte(token))
	if _, err := digest.Write([]byte(setupSessionPurpose)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
