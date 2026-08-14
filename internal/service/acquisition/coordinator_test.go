package acquisition_test

import (
	"context"
	"errors"
	"testing"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	acquisitionservice "github.com/kurtnissen/blackpearl/internal/service/acquisition"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorConfigureProbesBeforeSavingPrivateSettings(t *testing.T) {
	t.Parallel()
	repository := &fakeSettingsRepository{}
	provider := &fakeSearchProvider{}
	var factorySettings acquisitiondomain.SearchProviderSettings
	coordinator := newCoordinator(t, repository, &fakeTokenRepository{}, func(settings acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
		factorySettings = settings
		return provider, nil
	}, func(string) (acquisitionservice.CachedGateway, error) {
		return &fakeCachedGateway{}, nil
	}, &fakePublisher{})

	err := coordinator.Configure(context.Background(), "http://prowlarr:9696/base/", "private-key")

	require.NoError(t, err)
	require.Equal(t, 1, provider.readyCalls)
	require.Equal(t, "prowlarr", factorySettings.Provider())
	require.Equal(t, "http://prowlarr:9696/base/", factorySettings.Endpoint())
	require.Equal(t, "private-key", repository.saved.Credential())
}

func TestCoordinatorConfigureRejectsInvalidOrUnauthorizedSettingsWithoutSaving(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		endpoint   string
		credential string
		readyErr   error
		want       error
	}{
		{name: "invalid endpoint", endpoint: "relative", credential: "private", want: acquisitionservice.ErrInvalidSettings},
		{name: "invalid key", endpoint: "http://prowlarr:9696", credential: " private", want: acquisitionservice.ErrInvalidSettings},
		{name: "unauthorized", endpoint: "http://prowlarr:9696", credential: "private", readyErr: domain.ErrUnauthorized, want: acquisitionservice.ErrSearchUnauthorized},
		{name: "unavailable", endpoint: "http://prowlarr:9696", credential: "private", readyErr: errors.New("private provider path"), want: acquisitionservice.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := &fakeSettingsRepository{}
			provider := &fakeSearchProvider{readyErr: test.readyErr}
			coordinator := newCoordinator(t, repository, &fakeTokenRepository{}, func(acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
				return provider, nil
			}, func(string) (acquisitionservice.CachedGateway, error) {
				return &fakeCachedGateway{}, nil
			}, &fakePublisher{})

			err := coordinator.Configure(context.Background(), test.endpoint, test.credential)

			require.ErrorIs(t, err, test.want)
			require.Empty(t, repository.saved.Credential())
			require.NotContains(t, err.Error(), "private provider")
			require.NotContains(t, err.Error(), "path")
		})
	}
}

func TestCoordinatorStatusReturnsOnlyConfiguredState(t *testing.T) {
	t.Parallel()

	t.Run("not configured", func(t *testing.T) {
		t.Parallel()
		coordinator := newCoordinator(t, &fakeSettingsRepository{loadErr: domain.ErrNotFound}, &fakeTokenRepository{}, nilSearchFactory, nilGatewayFactory, &fakePublisher{})

		status, err := coordinator.Status(context.Background())

		require.NoError(t, err)
		require.False(t, status.Configured)
	})

	t.Run("configured", func(t *testing.T) {
		t.Parallel()
		settings := mustCoordinatorSettings(t)
		coordinator := newCoordinator(t, &fakeSettingsRepository{loaded: settings}, &fakeTokenRepository{}, nilSearchFactory, nilGatewayFactory, &fakePublisher{})

		status, err := coordinator.Status(context.Background())

		require.NoError(t, err)
		require.True(t, status.Configured)
	})

	t.Run("failure sanitized", func(t *testing.T) {
		t.Parallel()
		coordinator := newCoordinator(t, &fakeSettingsRepository{loadErr: errors.New("private settings path")}, &fakeTokenRepository{}, nilSearchFactory, nilGatewayFactory, &fakePublisher{})

		_, err := coordinator.Status(context.Background())

		require.ErrorIs(t, err, acquisitionservice.ErrUnavailable)
		require.NotContains(t, err.Error(), "private")
		require.NotContains(t, err.Error(), "path")
	})
}

func TestCoordinatorAcquireBuildsFreshProvidersFromSavedCredentials(t *testing.T) {
	t.Parallel()
	settings := mustCoordinatorSettings(t)
	repository := &fakeSettingsRepository{loaded: settings}
	configuration, err := domain.NewSetupConfiguration(mustCandidate(t, "17:1", "Existing.mkv", 10), "Existing", 2025)
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{configuration})
	require.NoError(t, err)
	tokens := &fakeTokenRepository{token: "saved-torbox-token", manifest: manifest}
	request, err := acquisitiondomain.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "ranked", "0123456789abcdef0123456789abcdef01234567")
	provider := &fakeSearchProvider{releases: []acquisitiondomain.Release{release}}
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "18")
	require.NoError(t, err)
	gateway := &fakeCachedGateway{
		cached: []acquisitiondomain.Release{release}, created: created,
		inspections: []inspectionResult{{items: []domain.MediaCandidate{mustCandidate(t, "18:2", "Example.Movie.2026.mkv", 20)}}},
	}
	publisher := &fakePublisher{}
	var factorySettings acquisitiondomain.SearchProviderSettings
	var factoryToken string
	coordinator := newCoordinator(t, repository, tokens, func(value acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
		factorySettings = value
		return provider, nil
	}, func(token string) (acquisitionservice.CachedGateway, error) {
		factoryToken = token
		return gateway, nil
	}, publisher)

	result, err := coordinator.Acquire(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, settings.Endpoint(), factorySettings.Endpoint())
	require.Equal(t, "saved-torbox-token", factoryToken)
	require.Equal(t, "18:2", result.Candidate().ObjectID)
	require.Len(t, publisher.published, 1)
}

func TestCoordinatorAcquireMapsMissingConfigurationAndSanitizesFactoryFailures(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	settings := mustCoordinatorSettings(t)
	configuration, err := domain.NewSetupConfiguration(mustCandidate(t, "17:1", "Existing.mkv", 10), "Existing", 2025)
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{configuration})
	require.NoError(t, err)
	tests := []struct {
		name           string
		settingsRepo   *fakeSettingsRepository
		tokenRepo      *fakeTokenRepository
		searchFactory  acquisitionservice.SearchProviderFactory
		gatewayFactory acquisitionservice.CachedGatewayFactory
		want           error
	}{
		{name: "settings missing", settingsRepo: &fakeSettingsRepository{loadErr: domain.ErrNotFound}, tokenRepo: &fakeTokenRepository{}, searchFactory: nilSearchFactory, gatewayFactory: nilGatewayFactory, want: domain.ErrNotConfigured},
		{name: "token missing", settingsRepo: &fakeSettingsRepository{loaded: settings}, tokenRepo: &fakeTokenRepository{loadErr: domain.ErrNotFound}, searchFactory: nilSearchFactory, gatewayFactory: nilGatewayFactory, want: acquisitionservice.ErrUnavailable},
		{name: "search factory", settingsRepo: &fakeSettingsRepository{loaded: settings}, tokenRepo: &fakeTokenRepository{token: "token", manifest: manifest}, searchFactory: func(acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
			return nil, errors.New("private search factory")
		}, gatewayFactory: nilGatewayFactory, want: acquisitionservice.ErrUnavailable},
		{name: "gateway factory", settingsRepo: &fakeSettingsRepository{loaded: settings}, tokenRepo: &fakeTokenRepository{token: "token", manifest: manifest}, searchFactory: func(acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
			return &fakeSearchProvider{}, nil
		}, gatewayFactory: func(string) (acquisitionservice.CachedGateway, error) {
			return nil, errors.New("private gateway factory")
		}, want: acquisitionservice.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			coordinator := newCoordinator(t, test.settingsRepo, test.tokenRepo, test.searchFactory, test.gatewayFactory, &fakePublisher{})

			_, err := coordinator.Acquire(context.Background(), request)

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, err.Error(), "factory")
		})
	}
}

func newCoordinator(
	t *testing.T,
	settings *fakeSettingsRepository,
	tokens *fakeTokenRepository,
	searchFactory acquisitionservice.SearchProviderFactory,
	gatewayFactory acquisitionservice.CachedGatewayFactory,
	publisher *fakePublisher,
) *acquisitionservice.Coordinator {
	t.Helper()
	coordinator, err := acquisitionservice.NewCoordinator(settings, tokens, searchFactory, gatewayFactory, publisher, acquisitionservice.Options{
		InspectionAttempts: 3, InspectionInterval: time.Nanosecond,
	})
	require.NoError(t, err)
	return coordinator
}

func mustCoordinatorSettings(t *testing.T) acquisitiondomain.SearchProviderSettings {
	t.Helper()
	settings, err := acquisitiondomain.NewSearchProviderSettings("prowlarr", "http://prowlarr:9696", "private-key")
	require.NoError(t, err)
	return settings
}

type fakeSettingsRepository struct {
	loaded  acquisitiondomain.SearchProviderSettings
	saved   acquisitiondomain.SearchProviderSettings
	loadErr error
	saveErr error
}

func (f *fakeSettingsRepository) Load(context.Context) (acquisitiondomain.SearchProviderSettings, error) {
	return f.loaded, f.loadErr
}

func (f *fakeSettingsRepository) Save(_ context.Context, settings acquisitiondomain.SearchProviderSettings) error {
	f.saved = settings
	return f.saveErr
}

type fakeTokenRepository struct {
	token    string
	manifest domain.SetupManifest
	loadErr  error
}

func (f *fakeTokenRepository) LoadManifest(context.Context) (string, domain.SetupManifest, error) {
	return f.token, f.manifest, f.loadErr
}

type fakeSearchProvider struct {
	releases   []acquisitiondomain.Release
	searchErr  error
	readyErr   error
	readyCalls int
}

func (*fakeSearchProvider) Name() string { return "prowlarr" }

func (*fakeSearchProvider) Capabilities() acquisitiondomain.ProviderCapabilities {
	return acquisitiondomain.NewProviderCapabilities([]acquisitiondomain.ReleaseProtocol{acquisitiondomain.ReleaseProtocolTorrent}, true, true, true)
}

func (f *fakeSearchProvider) Search(context.Context, acquisitiondomain.SearchRequest) ([]acquisitiondomain.Release, error) {
	return append([]acquisitiondomain.Release(nil), f.releases...), f.searchErr
}

func (f *fakeSearchProvider) Ready(context.Context) error {
	f.readyCalls++
	return f.readyErr
}

func nilSearchFactory(acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
	return &fakeSearchProvider{}, nil
}

func nilGatewayFactory(string) (acquisitionservice.CachedGateway, error) {
	return &fakeCachedGateway{}, nil
}
