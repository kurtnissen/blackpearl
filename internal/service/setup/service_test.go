package setup_test

import (
	"context"
	"errors"
	"fmt"
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

func TestServiceDiscoverMapsProviderAuthenticationFailure(t *testing.T) {
	t.Parallel()
	service := setupservice.New(&fakeSetupRepository{},
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{err: domain.ErrUnauthorized}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	_, err := service.Discover(context.Background(), "bad-token")

	require.ErrorIs(t, err, setupservice.ErrUnauthorized)
}

func TestServiceSetupAuthorizationRequiresSavedTokenOrIssuedBrowserSession(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)}
	service := newService(repository, nil)
	session, err := service.IssueSession(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, session, 64)

	require.NoError(t, service.AuthorizeSetup(context.Background(), "saved-token", "", ""))
	require.NoError(t, service.AuthorizeSetup(context.Background(), "", session, ""))
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "replacement-token", "wrong-session", ""), setupservice.ErrUnauthorized)
	require.True(t, service.Status().TokenConfigured)
}

func TestServiceSetupAuthorizationAllowsOnlyExplicitTokenBeforeFirstSave(t *testing.T) {
	t.Parallel()
	service := setupservice.New(&fakeSetupRepository{},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{}, "bootstrap-token",
	)

	require.NoError(t, service.AuthorizeSetup(context.Background(), "first-token", "", "bootstrap-token"))
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "first-token", "", "wrong-bootstrap"), setupservice.ErrUnauthorized)
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "", "invented-session", "bootstrap-token"), setupservice.ErrUnauthorized)
}

func TestServiceApplyPersistsThenPublishesValidatedSelection(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{}
	candidate := mustCandidate(t)
	runtime := &fakeCatalog{}
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(_ context.Context, token string, configuration domain.SetupConfiguration) (core.CatalogService, error) {
			require.Equal(t, "new-token", token)
			require.Equal(t, candidate.ObjectID, configuration.ObjectID)
			return runtime, nil
		},
		publisher,
	)

	selected, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, candidate.ObjectID, selected.ObjectID)
	require.Same(t, runtime, publisher.active)
	require.Equal(t, "new-token", repository.savedToken)
	require.Equal(t, candidate.ObjectID, repository.savedConfiguration.ObjectID)
	require.Equal(t, 1, publisher.calls)
	status := service.Status()
	require.False(t, status.SetupRequired)
	require.True(t, status.TokenConfigured)
	require.NotNil(t, status.Selected)
}

func TestServiceApplyKeepsRuntimeAndPersistenceWhenSaveOrPublishFails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		publishErr error
		saveErr    error
		wantCalls  int
	}{
		{name: "publish", publishErr: errors.New("NFS unavailable"), wantCalls: 1},
		{name: "save", saveErr: errors.New("disk full"), wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := mustCandidate(t)
			oldRuntime := &fakeCatalog{}
			newRuntime := &fakeCatalog{}
			repository := &fakeSetupRepository{saveErr: test.saveErr}
			publisher := &fakePublisher{active: oldRuntime, err: test.publishErr}
			service := setupservice.New(repository,
				func(string) (setupservice.Discoverer, error) {
					return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
				},
				func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
					return newRuntime, nil
				},
				publisher,
			)

			_, err := service.Apply(context.Background(), setupservice.ApplyRequest{Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026})

			require.Error(t, err)
			require.Same(t, oldRuntime, publisher.active)
			require.Equal(t, test.wantCalls, publisher.calls)
			require.Empty(t, repository.token)
		})
	}
}

func TestServiceApplyPublishesWhenRepositoryReportsPostCommitMaintenanceError(t *testing.T) {
	t.Parallel()
	candidate := mustCandidate(t)
	runtime := &fakeCatalog{}
	repository := &fakeSetupRepository{
		saveErr:           fmt.Errorf("inactive generation cleanup failed: %w", domain.ErrCleanupDeferred),
		commitOnSaveError: true,
	}
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return runtime, nil
		},
		publisher,
	)

	selected, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, "Example", selected.Title)
	require.Same(t, runtime, publisher.active)
}

func TestServiceApplyDoesNotPublishWhenRepositoryReportsDurabilityErrorAfterVisibleCommit(t *testing.T) {
	t.Parallel()
	candidate := mustCandidate(t)
	repository := &fakeSetupRepository{
		saveErr:           errors.New("sync setup root failed"),
		commitOnSaveError: true,
	}
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		publisher,
	)

	_, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.ErrorIs(t, err, setupservice.ErrUnavailable)
	require.Zero(t, publisher.calls)
}

func TestServiceRestoreActivatesSavedSelection(t *testing.T) {
	t.Parallel()
	configuration := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: configuration}
	runtime := &fakeCatalog{}
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{configuration.Candidate()}}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return runtime, nil
		},
		publisher,
	)

	err := service.Restore(context.Background())

	require.NoError(t, err)
	require.Same(t, runtime, publisher.active)
	require.False(t, service.Status().SetupRequired)
}

func TestServiceRestoreReportsSavedTokenWhenSelectedMediaIsUnavailable(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{readyErr: errors.New("media disappeared")}, nil
		},
		&fakePublisher{},
	)

	err := service.Restore(context.Background())

	require.Error(t, err)
	status := service.Status()
	require.True(t, status.TokenConfigured)
	require.True(t, status.SetupRequired)
}

func TestServiceApplyRestoresPriorSavedPairWhenPublishFails(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	candidate, err := domain.NewMediaCandidate("20:5", "Replacement.mp4", 2048)
	require.NoError(t, err)
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(context.Context, string, domain.SetupConfiguration) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{err: errors.New("NFS unavailable")},
	)

	_, err = service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "replacement-token", ObjectID: candidate.ObjectID, Title: "Replacement", Year: 2026,
	})

	require.Error(t, err)
	require.Equal(t, "saved-token", repository.token)
	require.Equal(t, previous, repository.configuration)
}

type fakeSetupRepository struct {
	token              string
	configuration      domain.SetupConfiguration
	loadedToken        string
	savedToken         string
	savedConfiguration domain.SetupConfiguration
	loadErr            error
	saveErr            error
	commitOnSaveError  bool
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
	if f.saveErr != nil {
		if f.commitOnSaveError {
			f.token = token
			f.configuration = configuration
		}
		return f.saveErr
	}
	f.token = token
	f.configuration = configuration
	return nil
}

func (f *fakeSetupRepository) Clear(context.Context) error {
	f.token = ""
	f.configuration = domain.SetupConfiguration{}
	return nil
}

type fakeDiscoverer struct {
	items []domain.MediaCandidate
	err   error
}

func (f *fakeDiscoverer) Discover(context.Context) ([]domain.MediaCandidate, error) {
	return f.items, f.err
}

type fakeCatalog struct{ readyErr error }

func (*fakeCatalog) List(context.Context) ([]domain.Media, error) { return nil, nil }
func (*fakeCatalog) Open(context.Context, string) (domain.ReadHandle, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCatalog) Ready(context.Context) error { return f.readyErr }

type fakePublisher struct {
	active core.CatalogService
	calls  int
	err    error
}

func (f *fakePublisher) Publish(_ context.Context, next core.CatalogService) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.active = next
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
		&fakePublisher{},
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
