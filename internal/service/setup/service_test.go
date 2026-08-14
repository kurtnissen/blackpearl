package setup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	setupservice "github.com/blackpearl-media/blackpearl/internal/service/setup"
	"github.com/stretchr/testify/require"
)

func TestServiceDiscoverUsesNewTokenWithoutPersistingIt(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{}
	candidate := mustCandidate(t)
	service := newService(repository, []domain.MediaCandidate{candidate})

	candidates, err := service.Discover(context.Background(), "new-token")

	require.NoError(t, err)
	require.Equal(t, []domain.MediaCandidate{candidate}, candidates)
	require.Empty(t, repository.savedToken)
}

func TestServiceDiscoverReusesSavedToken(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)}
	service := newService(repository, []domain.MediaCandidate{mustCandidate(t)})

	_, err := service.Discover(context.Background(), "")

	require.NoError(t, err)
	require.Equal(t, "saved-token", repository.loadedToken)
}

func TestServiceApplyActivatesValidatedSelectionAndPersistsAfterReload(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{}
	candidate := mustCandidate(t)
	runtime := &fakeCatalog{}
	switcher := &fakeSwitcher{}
	reloader := &fakeReloader{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(_ context.Context, token string, configuration domain.SetupConfiguration) (core.CatalogService, error) {
			require.Equal(t, "new-token", token)
			require.Equal(t, candidate.ObjectID, configuration.ObjectID)
			return runtime, nil
		},
		switcher, reloader,
	)

	selected, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, candidate.ObjectID, selected.ObjectID)
	require.Same(t, runtime, switcher.active)
	require.Equal(t, "new-token", repository.savedToken)
	require.Equal(t, candidate.ObjectID, repository.savedConfiguration.ObjectID)
	require.Equal(t, 1, reloader.calls)
	status := service.Status()
	require.False(t, status.SetupRequired)
	require.True(t, status.TokenConfigured)
	require.NotNil(t, status.Selected)
}

func TestServiceApplyRollsBackWhenReloadOrSaveFails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		reloadErr  error
		saveErr    error
		wantReload int
	}{
		{name: "reload", reloadErr: errors.New("NFS unavailable"), wantReload: 2},
		{name: "save", saveErr: errors.New("disk full"), wantReload: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := mustCandidate(t)
			oldRuntime := &fakeCatalog{}
			newRuntime := &fakeCatalog{}
			repository := &fakeSetupRepository{saveErr: test.saveErr}
			switcher := &fakeSwitcher{active: oldRuntime}
			reloader := &fakeReloader{firstErr: test.reloadErr}
			service := setupservice.New(repository,
				func(string) (setupservice.Discoverer, error) {
					return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
				},
				func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
					return newRuntime, nil
				},
				switcher, reloader,
			)

			_, err := service.Apply(context.Background(), setupservice.ApplyRequest{Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026})

			require.Error(t, err)
			require.Same(t, oldRuntime, switcher.active)
			require.Equal(t, test.wantReload, reloader.calls)
		})
	}
}

func TestServiceRestoreActivatesSavedSelection(t *testing.T) {
	t.Parallel()
	configuration := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: configuration}
	runtime := &fakeCatalog{}
	switcher := &fakeSwitcher{}
	reloader := &fakeReloader{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{configuration.Candidate()}}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return runtime, nil
		},
		switcher, reloader,
	)

	err := service.Restore(context.Background())

	require.NoError(t, err)
	require.Same(t, runtime, switcher.active)
	require.False(t, service.Status().SetupRequired)
}

type fakeSetupRepository struct {
	token              string
	configuration      domain.SetupConfiguration
	loadedToken        string
	savedToken         string
	savedConfiguration domain.SetupConfiguration
	loadErr            error
	saveErr            error
}

func (f *fakeSetupRepository) Load(context.Context) (string, domain.SetupConfiguration, error) {
	f.loadedToken = f.token
	if f.loadErr != nil {
		return "", domain.SetupConfiguration{}, f.loadErr
	}
	if f.token == "" {
		return "", domain.SetupConfiguration{}, domain.ErrNotFound
	}
	return f.token, f.configuration, nil
}

func (f *fakeSetupRepository) Save(_ context.Context, token string, configuration domain.SetupConfiguration) error {
	f.savedToken = token
	f.savedConfiguration = configuration
	return f.saveErr
}

type fakeDiscoverer struct {
	items []domain.MediaCandidate
	err   error
}

func (f *fakeDiscoverer) Discover(context.Context) ([]domain.MediaCandidate, error) {
	return f.items, f.err
}

type fakeCatalog struct{}

func (*fakeCatalog) List(context.Context) ([]domain.Media, error) { return nil, nil }
func (*fakeCatalog) Open(context.Context, string) (domain.ReadHandle, error) {
	return nil, domain.ErrNotFound
}
func (*fakeCatalog) Ready(context.Context) error { return nil }

type fakeSwitcher struct{ active core.CatalogService }

func (f *fakeSwitcher) Activate(next core.CatalogService) core.CatalogService {
	old := f.active
	f.active = next
	return old
}

type fakeReloader struct {
	calls    int
	firstErr error
}

func (f *fakeReloader) Reload(context.Context) error {
	f.calls++
	if f.calls == 1 {
		return f.firstErr
	}
	return nil
}

func newService(repository *fakeSetupRepository, items []domain.MediaCandidate) *setupservice.Service {
	return setupservice.New(repository,
		func(token string) (setupservice.Discoverer, error) {
			repository.loadedToken = token
			return &fakeDiscoverer{items: items}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakeSwitcher{}, &fakeReloader{},
	)
}

func mustCandidate(t *testing.T) domain.MediaCandidate {
	t.Helper()
	candidate, err := domain.NewMediaCandidate("17:3", "Example.mkv", 1024)
	require.NoError(t, err)
	return candidate
}

func mustConfiguration(t *testing.T) domain.SetupConfiguration {
	t.Helper()
	configuration, err := domain.NewSetupConfiguration(mustCandidate(t), "Example", 2026)
	require.NoError(t, err)
	return configuration
}
