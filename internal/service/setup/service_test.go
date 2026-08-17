package setup_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
	setupservice "github.com/kurtnissen/blackpearl/internal/service/setup"
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
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
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "replacement-token", "wrong-session", ""), setupservice.ErrSetupUnauthorized)
	require.True(t, service.Status().TokenConfigured)
}

func TestServiceSetupAuthorizationAllowsOnlyExplicitTokenBeforeFirstSave(t *testing.T) {
	t.Parallel()
	service := setupservice.New(&fakeSetupRepository{},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{}, "bootstrap-token",
	)

	require.NoError(t, service.AuthorizeSetup(context.Background(), "first-token", "", "bootstrap-token"))
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "first-token", "", "wrong-bootstrap"), setupservice.ErrSetupUnauthorized)
	require.ErrorIs(t, service.AuthorizeSetup(context.Background(), "", "invented-session", "bootstrap-token"), setupservice.ErrSetupUnauthorized)
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
		func(_ context.Context, token string, manifest domain.SetupManifest) (core.CatalogService, error) {
			require.Equal(t, "new-token", token)
			require.Equal(t, candidate.ObjectID, manifest.Items[0].ObjectID)
			return runtime, nil
		},
		publisher,
	)

	selected, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, candidate.ObjectID, selected.Items[0].ObjectID)
	require.Same(t, runtime, publisher.active)
	require.Equal(t, "new-token", repository.savedToken)
	require.Equal(t, candidate.ObjectID, repository.savedConfiguration.ObjectID)
	require.Equal(t, 1, publisher.calls)
	status := service.Status()
	require.False(t, status.SetupRequired)
	require.True(t, status.TokenConfigured)
	require.NotNil(t, status.Selected)
}

func TestServiceApplyPublishesMultipleSelectionsAsOneManifest(t *testing.T) {
	t.Parallel()
	first, err := domain.NewMediaCandidate("17:3", "First.mp4", 1024)
	require.NoError(t, err)
	second, err := domain.NewMediaCandidate("17:4", "Second.mkv", 2048)
	require.NoError(t, err)
	repository := &fakeSetupRepository{}
	runtime := &fakeCatalog{}
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{first, second}}, nil
		},
		func(_ context.Context, _ string, manifest domain.SetupManifest) (core.CatalogService, error) {
			require.Len(t, manifest.Items, 2)
			return runtime, nil
		},
		publisher,
	)

	manifest, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token",
		Items: []setupservice.ApplyItemRequest{
			{ObjectID: first.ObjectID, Title: "First", Year: 2024},
			{ObjectID: second.ObjectID, Title: "Second", Year: 2025},
		},
	})

	require.NoError(t, err)
	require.Len(t, manifest.Items, 2)
	require.Equal(t, manifest, repository.savedManifest)
	require.Same(t, runtime, publisher.active)
	status := service.Status()
	require.Len(t, status.SelectedItems, 2)
	require.Equal(t, status.SelectedItems[0], *status.Selected)
}

func TestServiceApplyBuildsExplicitEpisodeSelection(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("17:9", "Example.Show.S01E02.mkv", 2048)
	require.NoError(t, err)
	repository := &fakeSetupRepository{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(_ context.Context, _ string, manifest domain.SetupManifest) (core.CatalogService, error) {
			require.Equal(t, domain.MediaTypeEpisode, manifest.Items[0].MediaType)
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	manifest, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token",
		Items: []setupservice.ApplyItemRequest{{
			ObjectID: candidate.ObjectID, MediaType: domain.MediaTypeEpisode, Title: "The Second",
			Year: 2024, ShowTitle: "Example Show", Season: 1, Episode: 2,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, "Example Show", manifest.Items[0].ShowTitle)
	require.Equal(t, 1, manifest.Items[0].Season)
	require.Equal(t, 2, manifest.Items[0].Episode)
}

func TestServiceFindPublishedEpisodeUsesExactActiveManifestPath(t *testing.T) {
	t.Parallel()
	movieCandidate, err := domain.NewMediaCandidate("17:3", "Movie.mp4", 1024)
	require.NoError(t, err)
	movie, err := domain.NewSetupConfiguration(movieCandidate, "Example Movie", 2026)
	require.NoError(t, err)
	episodeCandidate, err := domain.NewMediaCandidate("17:4", "Episode.mp4", 2048)
	require.NoError(t, err)
	episode, err := domain.NewSetupEpisodeConfiguration(episodeCandidate, "Example Show", 2024, 1, 2, "The Second")
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{movie, episode})
	require.NoError(t, err)
	repository := &fakeSetupRepository{token: "saved-token", manifest: manifest}
	service := newService(repository, nil)
	require.NoError(t, service.Restore(context.Background()))
	episodePath, err := episode.VirtualPath()
	require.NoError(t, err)
	moviePath, err := movie.VirtualPath()
	require.NoError(t, err)

	published, found, err := service.FindPublishedEpisode(context.Background(), episodePath)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, episode, published)
	_, found, err = service.FindPublishedEpisode(context.Background(), moviePath)
	require.NoError(t, err)
	require.False(t, found)
	_, _, err = service.FindPublishedEpisode(context.Background(), "TV Shows/../private.mp4")
	require.Error(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = service.FindPublishedEpisode(canceled, episodePath)
	require.ErrorIs(t, err, context.Canceled)
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
				func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return runtime, nil
		},
		publisher,
	)

	selected, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.NoError(t, err)
	require.Equal(t, "Example", selected.Items[0].Title)
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		publisher,
	)

	_, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.ErrorIs(t, err, setupservice.ErrUnavailable)
	require.Zero(t, publisher.calls)
	require.Empty(t, repository.token)
	require.Empty(t, repository.manifest.Items)
}

func TestServiceApplyPreservesRuntimePreparationCauseForServerDiagnostics(t *testing.T) {
	t.Parallel()
	candidate := mustCandidate(t)
	service := setupservice.New(&fakeSetupRepository{},
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{candidate}}, nil
		},
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return nil, errors.New("TorBox CDN metadata requires status 200: got 206")
		},
		&fakePublisher{},
	)

	_, err := service.Apply(context.Background(), setupservice.ApplyRequest{
		Token: "new-token", ObjectID: candidate.ObjectID, Title: "Example", Year: 2026,
	})

	require.ErrorIs(t, err, setupservice.ErrUnavailable)
	require.ErrorContains(t, err, "TorBox CDN metadata requires status 200: got 206")
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return runtime, nil
		},
		publisher,
	)

	err := service.Restore(context.Background())

	require.NoError(t, err)
	require.Same(t, runtime, publisher.active)
	require.False(t, service.Status().SetupRequired)
}

func TestServiceRestorePublishesReachableSubsetWithoutChangingSavedManifest(t *testing.T) {
	t.Parallel()
	first := mustConfiguration(t)
	second := mustConfigurationWithID(t, "17:4", "Second.mkv", "Second", 2025)
	saved, err := domain.NewSetupManifest([]domain.SetupConfiguration{first, second})
	require.NoError(t, err)
	active, err := domain.NewSetupManifest([]domain.SetupConfiguration{first})
	require.NoError(t, err)
	repository := &fakeSetupRepository{token: "saved-token", manifest: saved}
	runtime := &fakeCatalog{}
	publisher := &fakePublisher{}
	service := setupservice.NewWithRestore(
		repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		func(context.Context, string, domain.SetupManifest) (setupservice.RestorePreparation, error) {
			return setupservice.RestorePreparation{Runtime: runtime, ActiveManifest: active}, nil
		},
		publisher,
	)

	err = service.Restore(context.Background())

	require.ErrorIs(t, err, setupservice.ErrDegraded)
	require.ErrorIs(t, err, setupservice.ErrUnavailable)
	require.Same(t, runtime, publisher.active)
	require.Equal(t, 1, publisher.calls)
	require.Equal(t, saved, repository.manifest)
	status := service.Status()
	require.False(t, status.SetupRequired)
	require.True(t, status.TokenConfigured)
	require.True(t, status.Degraded)
	require.Equal(t, 2, status.SavedItemCount)
	require.Equal(t, 1, status.ActiveItemCount)
	require.Equal(t, 1, status.UnavailableItemCount)
	require.Equal(t, active.Items, status.SelectedItems)
}

func TestServiceRestoreKeepsFirstPartialSnapshotUntilCompleteManifestIsReady(t *testing.T) {
	t.Parallel()
	first := mustConfiguration(t)
	second := mustConfigurationWithID(t, "17:4", "Second.mkv", "Second", 2025)
	saved, err := domain.NewSetupManifest([]domain.SetupConfiguration{first, second})
	require.NoError(t, err)
	firstOnly, err := domain.NewSetupManifest([]domain.SetupConfiguration{first})
	require.NoError(t, err)
	secondOnly, err := domain.NewSetupManifest([]domain.SetupConfiguration{second})
	require.NoError(t, err)
	repository := &fakeSetupRepository{token: "saved-token", manifest: saved}
	firstRuntime := &fakeCatalog{}
	secondRuntime := &fakeCatalog{}
	fullRuntime := &fakeCatalog{}
	preparations := []setupservice.RestorePreparation{
		{Runtime: firstRuntime, ActiveManifest: firstOnly},
		{Runtime: secondRuntime, ActiveManifest: secondOnly},
		{Runtime: fullRuntime, ActiveManifest: saved},
	}
	call := 0
	publisher := &fakePublisher{}
	service := setupservice.NewWithRestore(
		repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		func(context.Context, string, domain.SetupManifest) (setupservice.RestorePreparation, error) {
			preparation := preparations[call]
			call++
			return preparation, nil
		},
		publisher,
	)

	require.ErrorIs(t, service.Restore(context.Background()), setupservice.ErrDegraded)
	require.Same(t, firstRuntime, publisher.active)
	require.Equal(t, 1, publisher.calls)
	require.ErrorIs(t, service.Restore(context.Background()), setupservice.ErrDegraded)
	require.Same(t, firstRuntime, publisher.active)
	require.Equal(t, 1, publisher.calls)
	require.NoError(t, service.Restore(context.Background()))
	require.Same(t, fullRuntime, publisher.active)
	require.Equal(t, 2, publisher.calls)
	status := service.Status()
	require.False(t, status.Degraded)
	require.Equal(t, 2, status.SavedItemCount)
	require.Equal(t, 2, status.ActiveItemCount)
	require.Zero(t, status.UnavailableItemCount)
}

func TestServiceRestoreRejectsEmptyOrInvalidPartialPreparation(t *testing.T) {
	t.Parallel()
	first := mustConfiguration(t)
	second := mustConfigurationWithID(t, "17:4", "Second.mkv", "Second", 2025)
	saved, err := domain.NewSetupManifest([]domain.SetupConfiguration{first, second})
	require.NoError(t, err)
	notSaved := mustConfigurationWithID(t, "17:5", "Other.mkv", "Other", 2024)
	invalidManifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{notSaved})
	require.NoError(t, err)
	for _, test := range []struct {
		name        string
		preparation setupservice.RestorePreparation
	}{
		{name: "empty", preparation: setupservice.RestorePreparation{Runtime: &fakeCatalog{}}},
		{name: "not a saved subset", preparation: setupservice.RestorePreparation{Runtime: &fakeCatalog{}, ActiveManifest: invalidManifest}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publisher := &fakePublisher{}
			service := setupservice.NewWithRestore(
				&fakeSetupRepository{token: "saved-token", manifest: saved},
				func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
				func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
					return &fakeCatalog{}, nil
				},
				func(context.Context, string, domain.SetupManifest) (setupservice.RestorePreparation, error) {
					return test.preparation, nil
				},
				publisher,
			)

			err := service.Restore(context.Background())

			require.Error(t, err)
			require.NotErrorIs(t, err, setupservice.ErrDegraded)
			require.Zero(t, publisher.calls)
			require.True(t, service.Status().SetupRequired)
		})
	}
}

func TestServiceRestorePreservesFatalPreparationCause(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{}
	service := setupservice.NewWithRestore(
		&fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		func(context.Context, string, domain.SetupManifest) (setupservice.RestorePreparation, error) {
			return setupservice.RestorePreparation{}, domain.ErrUnauthorized
		},
		publisher,
	)

	err := service.Restore(context.Background())

	require.ErrorIs(t, err, domain.ErrUnauthorized)
	require.NotErrorIs(t, err, setupservice.ErrUnavailable)
	require.Zero(t, publisher.calls)
	require.True(t, service.Status().TokenConfigured)
}

func TestServiceRestoreSerializesConcurrentApply(t *testing.T) {
	repository := &fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)}
	replacement, err := domain.NewMediaCandidate("17:4", "Replacement.mkv", 2048)
	require.NoError(t, err)
	restoreEntered := make(chan struct{})
	releaseRestore := make(chan struct{})
	applyEntered := make(chan struct{})
	service := setupservice.NewWithRestore(
		repository,
		func(string) (setupservice.Discoverer, error) {
			return &fakeDiscoverer{items: []domain.MediaCandidate{replacement}}, nil
		},
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			close(applyEntered)
			return &fakeCatalog{}, nil
		},
		func(_ context.Context, _ string, manifest domain.SetupManifest) (setupservice.RestorePreparation, error) {
			close(restoreEntered)
			<-releaseRestore
			return setupservice.RestorePreparation{Runtime: &fakeCatalog{}, ActiveManifest: manifest}, nil
		},
		&fakePublisher{},
	)
	restoreResult := make(chan error, 1)
	go func() { restoreResult <- service.Restore(context.Background()) }()
	<-restoreEntered
	applyResult := make(chan error, 1)
	go func() {
		_, applyErr := service.Apply(context.Background(), setupservice.ApplyRequest{
			Token: "saved-token", ObjectID: replacement.ObjectID, Title: "Replacement", Year: 2026,
		})
		applyResult <- applyErr
	}()

	select {
	case <-applyEntered:
		t.Fatal("apply entered runtime preparation before restore released the transition lock")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRestore)

	require.NoError(t, <-restoreResult)
	require.NoError(t, <-applyResult)
	select {
	case <-applyEntered:
	default:
		t.Fatal("apply never entered runtime preparation after restore completed")
	}
}

func TestServiceDegradedRestoreKeepsDurableDeduplicationSeparateFromActivePlayback(t *testing.T) {
	t.Parallel()
	movie := mustConfiguration(t)
	episodeCandidate, err := domain.NewMediaCandidate("17:4", "Example.Show.S01E02.mkv", 2048)
	require.NoError(t, err)
	episode, err := domain.NewSetupEpisodeConfiguration(episodeCandidate, "Example Show", 2024, 1, 2, "The Second")
	require.NoError(t, err)
	saved, err := domain.NewSetupManifest([]domain.SetupConfiguration{movie, episode})
	require.NoError(t, err)
	active, err := domain.NewSetupManifest([]domain.SetupConfiguration{movie})
	require.NoError(t, err)
	service := setupservice.NewWithRestore(
		&fakeSetupRepository{token: "saved-token", manifest: saved},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		func(context.Context, string, domain.SetupManifest) (setupservice.RestorePreparation, error) {
			return setupservice.RestorePreparation{Runtime: &fakeCatalog{}, ActiveManifest: active}, nil
		},
		&fakePublisher{},
	)
	require.ErrorIs(t, service.Restore(context.Background()), setupservice.ErrDegraded)
	search, err := acquisitiondomain.NewEpisodeSearch("Example Show", 2024, 1, 2)
	require.NoError(t, err)
	episodePath, err := episode.VirtualPath()
	require.NoError(t, err)

	objectID, found, err := service.FindPublished(context.Background(), search)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, episode.ObjectID, objectID)
	_, found, err = service.FindPublishedEpisode(context.Background(), episodePath)
	require.NoError(t, err)
	require.False(t, found)
}

func TestServiceRestoreReportsSavedTokenWhenSelectedMediaIsUnavailable(t *testing.T) {
	t.Parallel()
	repository := &fakeSetupRepository{token: "saved-token", configuration: mustConfiguration(t)}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
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
	manifest           domain.SetupManifest
	savedManifest      domain.SetupManifest
	loadErr            error
	saveErr            error
	commitOnSaveError  bool
}

func (f *fakeSetupRepository) LoadManifest(context.Context) (string, domain.SetupManifest, error) {
	f.loadedToken = f.token
	if f.loadErr != nil {
		return "", domain.SetupManifest{}, f.loadErr
	}
	if f.token == "" {
		return "", domain.SetupManifest{}, domain.ErrNotFound
	}
	if len(f.manifest.Items) > 0 {
		return f.token, f.manifest, nil
	}
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{f.configuration})
	if err != nil {
		return "", domain.SetupManifest{}, err
	}
	return f.token, manifest, nil
}

func (f *fakeSetupRepository) SaveManifest(_ context.Context, token string, manifest domain.SetupManifest) error {
	f.savedToken = token
	f.savedManifest = manifest
	if len(manifest.Items) > 0 {
		f.savedConfiguration = manifest.Items[0]
	}
	if f.saveErr != nil {
		if f.commitOnSaveError {
			f.token = token
			f.manifest = manifest
			f.configuration = manifest.Items[0]
		}
		return f.saveErr
	}
	f.token = token
	f.manifest = manifest
	f.configuration = manifest.Items[0]
	return nil
}

func (f *fakeSetupRepository) Clear(context.Context) error {
	f.token = ""
	f.configuration = domain.SetupConfiguration{}
	f.manifest = domain.SetupManifest{}
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
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
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

func mustConfigurationWithID(t *testing.T, objectID string, name string, title string, year int) domain.SetupConfiguration {
	t.Helper()
	candidate, err := domain.NewMediaCandidate(objectID, name, 2048)
	require.NoError(t, err)
	configuration, err := domain.NewSetupConfiguration(candidate, title, year)
	require.NoError(t, err)
	return configuration
}
