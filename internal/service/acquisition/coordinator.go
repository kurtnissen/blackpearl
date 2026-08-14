package acquisition

import (
	"context"
	"errors"
	"fmt"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/resolver"
)

var (
	// ErrInvalidSettings indicates malformed search-provider connection input.
	ErrInvalidSettings = errors.New("search provider settings are invalid")
	// ErrSearchUnauthorized indicates that the configured search provider
	// rejected its credential during a readiness probe.
	ErrSearchUnauthorized = errors.New("search provider credentials were rejected")
)

// SettingsRepository stores one private search-provider connection.
type SettingsRepository interface {
	Load(ctx context.Context) (acquisitiondomain.SearchProviderSettings, error)
	Save(ctx context.Context, settings acquisitiondomain.SearchProviderSettings) error
}

// TokenRepository supplies the existing saved TorBox token without exposing it
// through the coordinator result.
type TokenRepository interface {
	LoadManifest(ctx context.Context) (string, domain.SetupManifest, error)
}

// ReadySearchProvider is one configured resolver provider with a read-only
// connection probe.
type ReadySearchProvider interface {
	Name() string
	Capabilities() acquisitiondomain.ProviderCapabilities
	Search(ctx context.Context, request acquisitiondomain.SearchRequest) ([]acquisitiondomain.Release, error)
	Ready(ctx context.Context) error
}

// SearchProviderFactory constructs an isolated search gateway from saved settings.
type SearchProviderFactory func(settings acquisitiondomain.SearchProviderSettings) (ReadySearchProvider, error)

// CachedGatewayFactory constructs an isolated cached acquisition gateway from
// the saved TorBox token.
type CachedGatewayFactory func(token string) (CachedGateway, error)

// CoordinatorStatus contains no endpoint or credential data.
type CoordinatorStatus struct {
	Configured bool `json:"configured"`
}

// Coordinator owns private provider configuration and per-request dependency composition.
type Coordinator struct {
	settingsRepository SettingsRepository
	tokenRepository    TokenRepository
	searchFactory      SearchProviderFactory
	gatewayFactory     CachedGatewayFactory
	publisher          Publisher
	options            Options
}

// NewCoordinator constructs a configured-acquisition coordinator.
func NewCoordinator(
	settingsRepository SettingsRepository,
	tokenRepository TokenRepository,
	searchFactory SearchProviderFactory,
	gatewayFactory CachedGatewayFactory,
	publisher Publisher,
	options Options,
) (*Coordinator, error) {
	if settingsRepository == nil || tokenRepository == nil || searchFactory == nil || gatewayFactory == nil || publisher == nil {
		return nil, errors.New("acquisition coordinator dependencies are required")
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	return &Coordinator{
		settingsRepository: settingsRepository, tokenRepository: tokenRepository,
		searchFactory: searchFactory, gatewayFactory: gatewayFactory, publisher: publisher, options: options,
	}, nil
}

// Status reports only whether private search-provider settings exist.
func (c *Coordinator) Status(ctx context.Context) (CoordinatorStatus, error) {
	if err := ctx.Err(); err != nil {
		return CoordinatorStatus{}, fmt.Errorf("read acquisition status: %w", err)
	}
	_, err := c.settingsRepository.Load(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return CoordinatorStatus{Configured: false}, nil
	}
	if err != nil {
		return CoordinatorStatus{}, fmt.Errorf("read acquisition status: %w", ErrUnavailable)
	}
	return CoordinatorStatus{Configured: true}, nil
}

// Configure validates and probes Prowlarr before atomically saving its private connection.
func (c *Coordinator) Configure(ctx context.Context, endpoint string, credential string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("configure acquisition search: %w", err)
	}
	settings, err := acquisitiondomain.NewSearchProviderSettings("prowlarr", endpoint, credential)
	if err != nil {
		return ErrInvalidSettings
	}
	provider, err := c.searchFactory(settings)
	if err != nil {
		return fmt.Errorf("construct acquisition search provider: %w", ErrUnavailable)
	}
	if err := provider.Ready(ctx); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return fmt.Errorf("probe acquisition search provider: %w", context.Canceled)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("probe acquisition search provider: %w", context.DeadlineExceeded)
		case errors.Is(err, domain.ErrUnauthorized):
			return ErrSearchUnauthorized
		default:
			return fmt.Errorf("probe acquisition search provider: %w", ErrUnavailable)
		}
	}
	if err := c.settingsRepository.Save(ctx, settings); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("save acquisition search settings: %w", contextErr)
		}
		return fmt.Errorf("save acquisition search settings: %w", ErrUnavailable)
	}
	return nil
}

// Acquire builds fresh provider gateways from saved credentials and runs one
// ranked cached-only acquisition transaction.
func (c *Coordinator) Acquire(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error) {
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("coordinate acquisition: %w", err)
	}
	settings, err := c.settingsRepository.Load(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return acquisitiondomain.AcquiredMedia{}, domain.ErrNotConfigured
	}
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("load acquisition search settings: %w", ErrUnavailable)
	}
	token, _, err := c.tokenRepository.LoadManifest(ctx)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("load acquisition account credentials: %w", ErrUnavailable)
	}
	provider, err := c.searchFactory(settings)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("construct acquisition search provider: %w", ErrUnavailable)
	}
	gateway, err := c.gatewayFactory(token)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("construct cached acquisition provider: %w", ErrUnavailable)
	}
	searcher := resolver.NewSearcher(provider)
	service, err := New(searcher, gateway, c.publisher, c.options)
	if err != nil {
		return acquisitiondomain.AcquiredMedia{}, fmt.Errorf("construct acquisition transaction: %w", ErrUnavailable)
	}
	return service.Acquire(ctx, request)
}
