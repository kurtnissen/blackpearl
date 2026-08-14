// Package setup orchestrates browser-driven provider discovery and runtime activation.
package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

var (
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
}

// Discoverer lists eligible account media without requesting content bytes.
type Discoverer interface {
	Discover(ctx context.Context) ([]domain.MediaCandidate, error)
}

// GatewayFactory constructs an isolated discovery gateway for one token.
type GatewayFactory func(token string) (Discoverer, error)

// RuntimeFactory prepares and validates a range-backed catalog without activating it.
type RuntimeFactory func(ctx context.Context, token string, configuration domain.SetupConfiguration) (core.CatalogService, error)

// Activator atomically replaces the catalog used by new filesystem operations.
type Activator interface {
	Activate(next core.CatalogService) core.CatalogService
}

// Reloader publishes the currently active catalog namespace.
type Reloader interface {
	Reload(ctx context.Context) error
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
	activator      Activator
	reloader       Reloader

	mu              sync.RWMutex
	transitionMu    sync.Mutex
	tokenConfigured bool
	selected        *domain.SetupConfiguration
}

// New constructs a setup service from narrow infrastructure boundaries.
func New(repository Repository, gatewayFactory GatewayFactory, runtimeFactory RuntimeFactory, activator Activator, reloader Reloader) *Service {
	return &Service{
		repository: repository, gatewayFactory: gatewayFactory, runtimeFactory: runtimeFactory,
		activator: activator, reloader: reloader,
	}
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
		return nil, fmt.Errorf("discover provider media: %w", ErrUnavailable)
	}
	return items, nil
}

// Apply validates, activates, reloads, and then persists one selected video.
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
	previous := s.activator.Activate(runtime)
	if err := s.reloader.Reload(ctx); err != nil {
		return domain.SetupConfiguration{}, s.rollback(ctx, previous, "reload selected media", err)
	}
	if err := s.repository.Save(ctx, token, configuration); err != nil {
		return domain.SetupConfiguration{}, s.rollback(ctx, previous, "persist selected media", err)
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
	runtime, err := s.runtimeFactory(ctx, token, configuration)
	if err != nil {
		return fmt.Errorf("prepare saved setup: %w", ErrUnavailable)
	}
	if err := runtime.Ready(ctx); err != nil {
		return fmt.Errorf("probe saved setup: %w", ErrUnavailable)
	}
	previous := s.activator.Activate(runtime)
	if err := s.reloader.Reload(ctx); err != nil {
		return s.rollback(ctx, previous, "reload saved setup", err)
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
	return token, nil
}

func (s *Service) rollback(ctx context.Context, previous core.CatalogService, operation string, cause error) error {
	s.activator.Activate(previous)
	rollbackErr := s.reloader.Reload(ctx)
	return errors.Join(fmt.Errorf("%s: %w", operation, cause), rollbackErr)
}

func (s *Service) setStatus(configuration domain.SetupConfiguration) {
	s.mu.Lock()
	copy := configuration
	s.selected = &copy
	s.tokenConfigured = true
	s.mu.Unlock()
}
