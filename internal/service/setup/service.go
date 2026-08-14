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
	"strings"
	"sync"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
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
	// ErrInvalidSelection indicates that the requested object is not eligible.
	ErrInvalidSelection = errors.New("selected media is not available")
)

// Repository stores one token and one selected configuration.
type Repository interface {
	Load(ctx context.Context) (string, domain.SetupConfiguration, error)
	Save(ctx context.Context, token string, configuration domain.SetupConfiguration) error
	Clear(ctx context.Context) error
}

// Discoverer lists eligible account media without requesting content bytes.
type Discoverer interface {
	Discover(ctx context.Context) ([]domain.MediaCandidate, error)
}

// GatewayFactory constructs an isolated discovery gateway for one token.
type GatewayFactory func(token string) (Discoverer, error)

// RuntimeFactory prepares and validates a range-backed catalog without activating it.
type RuntimeFactory func(ctx context.Context, token string, configuration domain.SetupConfiguration) (core.CatalogService, error)

// Publisher atomically publishes one namespace-and-catalog runtime snapshot.
type Publisher interface {
	Publish(ctx context.Context, next core.CatalogService) error
}

// ApplyRequest contains write-only credentials and public Plex metadata.
type ApplyRequest struct {
	Token    string `json:"token,omitempty"`
	ObjectID string `json:"objectId"`
	Title    string `json:"title"`
	Year     int    `json:"year"`
}

// Status is safe to return to an untrusted browser.
type Status struct {
	SetupRequired   bool                       `json:"setupRequired"`
	TokenConfigured bool                       `json:"tokenConfigured"`
	Selected        *domain.SetupConfiguration `json:"selected,omitempty"`
}

// Service owns setup state transitions.
type Service struct {
	repository     Repository
	gatewayFactory GatewayFactory
	runtimeFactory RuntimeFactory
	publisher      Publisher
	bootstrapToken string

	mu              sync.RWMutex
	transitionMu    sync.Mutex
	tokenConfigured bool
	selected        *domain.SetupConfiguration
}

// New constructs a setup service from narrow infrastructure boundaries.
func New(repository Repository, gatewayFactory GatewayFactory, runtimeFactory RuntimeFactory, publisher Publisher, bootstrapToken ...string) *Service {
	service := &Service{
		repository: repository, gatewayFactory: gatewayFactory, runtimeFactory: runtimeFactory,
		publisher: publisher,
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
	status := Status{SetupRequired: s.selected == nil, TokenConfigured: s.tokenConfigured}
	if s.selected != nil {
		copy := *s.selected
		status.Selected = &copy
	}
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
	savedToken, _, err := s.repository.Load(ctx)
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

// Apply validates and probes one selected video, persists its credentials and
// metadata, then atomically publishes the prepared runtime.
func (s *Service) Apply(ctx context.Context, request ApplyRequest) (domain.SetupConfiguration, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	token, err := s.resolveToken(ctx, request.Token)
	if err != nil {
		return domain.SetupConfiguration{}, err
	}
	items, err := s.discoverWithToken(ctx, token)
	if err != nil {
		return domain.SetupConfiguration{}, err
	}
	var selected *domain.MediaCandidate
	for index := range items {
		if items[index].ObjectID == request.ObjectID {
			copy := items[index]
			selected = &copy
			break
		}
	}
	if selected == nil {
		return domain.SetupConfiguration{}, ErrInvalidSelection
	}
	configuration, err := domain.NewSetupConfiguration(*selected, request.Title, request.Year)
	if err != nil {
		return domain.SetupConfiguration{}, fmt.Errorf("validate selected media: %w", ErrInvalidSelection)
	}
	runtime, err := s.runtimeFactory(ctx, token, configuration)
	if err != nil {
		return domain.SetupConfiguration{}, fmt.Errorf("prepare selected media: %w", ErrUnavailable)
	}
	if err := runtime.Ready(ctx); err != nil {
		return domain.SetupConfiguration{}, fmt.Errorf("probe selected media: %w", ErrUnavailable)
	}
	previousToken, previousConfiguration, loadErr := s.repository.Load(ctx)
	hadPrevious := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, domain.ErrNotFound) {
		return domain.SetupConfiguration{}, fmt.Errorf("load prior setup before commit: %w", ErrUnavailable)
	}
	if err := s.repository.Save(ctx, token, configuration); err != nil {
		if !errors.Is(err, domain.ErrCleanupDeferred) {
			return domain.SetupConfiguration{}, fmt.Errorf("persist selected media: %w", ErrUnavailable)
		}
		committedToken, committedConfiguration, loadAfterSaveErr := s.repository.Load(ctx)
		if loadAfterSaveErr != nil || committedToken != token || committedConfiguration != configuration {
			return domain.SetupConfiguration{}, fmt.Errorf("persist selected media: %w", ErrUnavailable)
		}
	}
	if err := s.publisher.Publish(ctx, runtime); err != nil {
		rollbackErr := s.rollbackPersistence(ctx, hadPrevious, previousToken, previousConfiguration)
		return domain.SetupConfiguration{}, errors.Join(fmt.Errorf("publish selected media: %w", ErrUnavailable), rollbackErr)
	}
	s.setStatus(configuration)
	return configuration, nil
}

// Restore activates securely persisted setup state during process startup.
func (s *Service) Restore(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	token, configuration, err := s.repository.Load(ctx)
	if err != nil {
		return err
	}
	s.markTokenConfigured()
	runtime, err := s.runtimeFactory(ctx, token, configuration)
	if err != nil {
		return fmt.Errorf("prepare saved setup: %w", ErrUnavailable)
	}
	if err := runtime.Ready(ctx); err != nil {
		return fmt.Errorf("probe saved setup: %w", ErrUnavailable)
	}
	if err := s.publisher.Publish(ctx, runtime); err != nil {
		return fmt.Errorf("publish saved setup: %w", ErrUnavailable)
	}
	s.setStatus(configuration)
	return nil
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
	token, _, err := s.repository.Load(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", ErrUnauthorized
		}
		return "", fmt.Errorf("load saved provider credentials: %w", ErrUnavailable)
	}
	s.markTokenConfigured()
	return token, nil
}

func (s *Service) rollbackPersistence(ctx context.Context, hadPrevious bool, token string, configuration domain.SetupConfiguration) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if hadPrevious {
		if err := s.repository.Save(rollbackContext, token, configuration); err != nil {
			return fmt.Errorf("restore prior setup persistence: %w", err)
		}
		return nil
	}
	if err := s.repository.Clear(rollbackContext); err != nil {
		return fmt.Errorf("clear failed setup persistence: %w", err)
	}
	return nil
}

func (s *Service) setStatus(configuration domain.SetupConfiguration) {
	s.mu.Lock()
	copy := configuration
	s.selected = &copy
	s.tokenConfigured = true
	s.mu.Unlock()
}

func (s *Service) markTokenConfigured() {
	s.mu.Lock()
	s.tokenConfigured = true
	s.mu.Unlock()
}

func sessionForToken(token string) (string, error) {
	digest := hmac.New(sha256.New, []byte(token))
	if _, err := digest.Write([]byte(setupSessionPurpose)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
