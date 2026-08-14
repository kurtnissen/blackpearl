package setup_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
	setupservice "github.com/kurtnissen/blackpearl/internal/service/setup"
	"github.com/stretchr/testify/require"
)

func TestServicePublishAcquiredAppendsAndAtomicallyPublishesMovie(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	media := mustAcquiredMovie(t, "18:4", "Second.Movie.2025.mkv", "Second Movie", 2025)
	runtime := &fakeCatalog{}
	publisher := &fakePublisher{}
	var prepared domain.SetupManifest
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(_ context.Context, token string, manifest domain.SetupManifest) (core.CatalogService, error) {
			require.Equal(t, "saved-token", token)
			prepared = manifest
			return runtime, nil
		},
		publisher,
	)

	err := service.PublishAcquired(context.Background(), media)

	require.NoError(t, err)
	require.Len(t, prepared.Items, 2)
	require.Equal(t, previous, prepared.Items[0])
	require.Equal(t, "18:4", prepared.Items[1].ObjectID)
	require.Equal(t, prepared, repository.savedManifest)
	require.Same(t, runtime, publisher.active)
	require.Equal(t, prepared.Items, service.Status().SelectedItems)
}

func TestServicePublishAcquiredReplacesSameLogicalMediaPath(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	media := mustAcquiredMovie(t, "18:4", "Example.2026.1080p.mkv", "Example", 2026)
	var prepared domain.SetupManifest
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(_ context.Context, _ string, manifest domain.SetupManifest) (core.CatalogService, error) {
			prepared = manifest
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	err := service.PublishAcquired(context.Background(), media)

	require.NoError(t, err)
	require.Len(t, prepared.Items, 1)
	require.Equal(t, "18:4", prepared.Items[0].ObjectID)
	require.Equal(t, "Example", prepared.Items[0].Title)
}

func TestServicePublishAcquiredKeysObjectReplacementByProvider(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	request, err := acquisitiondomain.NewMovieSearch("Archive Movie", 2025)
	require.NoError(t, err)
	backing, err := domain.NewBackingRef("internet-archive-file", previous.ObjectID)
	require.NoError(t, err)
	candidate, err := domain.NewProviderMediaCandidate(backing, "Archive.Movie.2025.mp4", 1024)
	require.NoError(t, err)
	media, err := acquisitiondomain.NewRangeAcquiredMedia(request, candidate)
	require.NoError(t, err)
	var prepared domain.SetupManifest
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(_ context.Context, _ string, manifest domain.SetupManifest) (core.CatalogService, error) {
			prepared = manifest
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	err = service.PublishAcquired(context.Background(), media)

	require.NoError(t, err)
	require.Len(t, prepared.Items, 2)
	require.Equal(t, previous.Backing(), prepared.Items[0].Backing())
	require.Equal(t, backing, prepared.Items[1].Backing())
}

func TestServiceFindPublishedMovieReadsCurrentManifest(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	objectID, found, err := service.FindPublishedMovie(context.Background(), previous.Title, previous.Year)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, previous.ObjectID, objectID)

	objectID, found, err = service.FindPublishedMovie(context.Background(), "Something Else", previous.Year)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, objectID)
}

func TestServiceFindPublishedReadsExactEpisodeFromCurrentManifest(t *testing.T) {
	t.Parallel()
	candidate, err := domain.NewMediaCandidate("22:7", "Example.Show.S01E01.mkv", 1024)
	require.NoError(t, err)
	episode, err := domain.NewSetupEpisodeConfiguration(candidate, "Example Show", 2026, 1, 1, "Pilot")
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{episode})
	require.NoError(t, err)
	repository := &fakeSetupRepository{token: "saved-token", manifest: manifest}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)
	request, err := acquisitiondomain.NewEpisodeSearch("Example Show", 2026, 1, 1)
	require.NoError(t, err)

	objectID, found, err := service.FindPublished(context.Background(), request)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, episode.ObjectID, objectID)

	nextEpisode, err := acquisitiondomain.NewEpisodeSearch("Example Show", 2026, 1, 2)
	require.NoError(t, err)
	objectID, found, err = service.FindPublished(context.Background(), nextEpisode)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, objectID)
}

func TestServiceFindPublishedMovieTreatsMissingSetupAsEmpty(t *testing.T) {
	t.Parallel()
	service := setupservice.New(&fakeSetupRepository{},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	objectID, found, err := service.FindPublishedMovie(context.Background(), "Example", 2026)

	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, objectID)
}

func TestServiceFindPublishedMovieMapsRepositoryFailure(t *testing.T) {
	t.Parallel()
	service := setupservice.New(&fakeSetupRepository{loadErr: errors.New("disk failed")},
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	_, _, err := service.FindPublishedMovie(context.Background(), "Example", 2026)

	require.ErrorIs(t, err, setupservice.ErrUnavailable)
}

func TestServicePublishAcquiredMapsEpisodeIntent(t *testing.T) {
	t.Parallel()
	previous := mustConfiguration(t)
	repository := &fakeSetupRepository{token: "saved-token", configuration: previous}
	media := mustAcquiredEpisode(t, "18:4", "Example.Show.S07E02.mkv", "Example Show", 2026, 7, 2)
	var prepared domain.SetupManifest
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(_ context.Context, _ string, manifest domain.SetupManifest) (core.CatalogService, error) {
			prepared = manifest
			return &fakeCatalog{}, nil
		},
		&fakePublisher{},
	)

	err := service.PublishAcquired(context.Background(), media)

	require.NoError(t, err)
	require.Len(t, prepared.Items, 2)
	episode := prepared.Items[1]
	require.Equal(t, domain.MediaTypeEpisode, episode.MediaType)
	require.Equal(t, "Example Show", episode.ShowTitle)
	require.Equal(t, "Episode 2", episode.Title)
	require.Equal(t, 7, episode.Season)
	require.Equal(t, 2, episode.Episode)
}

func TestServicePublishAcquiredRejectsDistinctItemAtManifestCapacity(t *testing.T) {
	t.Parallel()
	items := make([]domain.SetupConfiguration, 0, domain.MaximumSetupManifestItems)
	for index := 1; index <= domain.MaximumSetupManifestItems; index++ {
		candidate, err := domain.NewMediaCandidate(fmt.Sprintf("%d:1", index), fmt.Sprintf("Movie.%d.mkv", index), int64(index))
		require.NoError(t, err)
		configuration, err := domain.NewSetupConfiguration(candidate, fmt.Sprintf("Movie %d", index), 2026)
		require.NoError(t, err)
		items = append(items, configuration)
	}
	manifest, err := domain.NewSetupManifest(items)
	require.NoError(t, err)
	repository := &fakeSetupRepository{token: "saved-token", manifest: manifest}
	media := mustAcquiredMovie(t, "101:1", "Another.Movie.mkv", "Another Movie", 2026)
	publisher := &fakePublisher{}
	service := setupservice.New(repository,
		func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
		func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
			t.Fatal("runtime must not be prepared beyond manifest capacity")
			return nil, nil
		},
		publisher,
	)

	err = service.PublishAcquired(context.Background(), media)

	require.ErrorIs(t, err, setupservice.ErrInvalidSelection)
	require.Zero(t, publisher.calls)
	require.Equal(t, manifest, repository.manifest)
}

func TestServicePublishAcquiredPreservesPriorManifestOnTransactionFailures(t *testing.T) {
	t.Parallel()
	media := mustAcquiredMovie(t, "18:4", "Second.Movie.mkv", "Second Movie", 2025)
	tests := []struct {
		name       string
		prepareErr error
		readyErr   error
		saveErr    error
		publishErr error
	}{
		{name: "prepare", prepareErr: errors.New("prepare failed")},
		{name: "ready", readyErr: errors.New("ready failed")},
		{name: "save", saveErr: errors.New("save failed")},
		{name: "publish", publishErr: errors.New("publish failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			previous := mustConfiguration(t)
			repository := &fakeSetupRepository{token: "saved-token", configuration: previous, saveErr: test.saveErr}
			publisher := &fakePublisher{err: test.publishErr}
			service := setupservice.New(repository,
				func(string) (setupservice.Discoverer, error) { return &fakeDiscoverer{}, nil },
				func(context.Context, string, domain.SetupManifest) (core.CatalogService, error) {
					if test.prepareErr != nil {
						return nil, test.prepareErr
					}
					return &fakeCatalog{readyErr: test.readyErr}, nil
				},
				publisher,
			)

			err := service.PublishAcquired(context.Background(), media)

			require.Error(t, err)
			require.Equal(t, "saved-token", repository.token)
			require.Equal(t, previous, repository.configuration)
			require.Empty(t, service.Status().SelectedItems)
		})
	}
}

func mustAcquiredMovie(t *testing.T, objectID string, name string, title string, year int) acquisitiondomain.AcquiredMedia {
	t.Helper()
	request, err := acquisitiondomain.NewMovieSearch(title, year)
	require.NoError(t, err)
	return mustAcquired(t, request, objectID, name)
}

func mustAcquiredEpisode(t *testing.T, objectID string, name string, showTitle string, year int, season int, episode int) acquisitiondomain.AcquiredMedia {
	t.Helper()
	request, err := acquisitiondomain.NewEpisodeSearch(showTitle, year, season, episode)
	require.NoError(t, err)
	return mustAcquired(t, request, objectID, name)
}

func mustAcquired(t *testing.T, request acquisitiondomain.SearchRequest, objectID string, name string) acquisitiondomain.AcquiredMedia {
	t.Helper()
	release, err := acquisitiondomain.NewRelease(acquisitiondomain.ReleaseInput{
		Provider: "prowlarr", SourceID: "release", Title: request.Query(), Protocol: acquisitiondomain.ReleaseProtocolTorrent,
		Size: 1024, Indexer: "authorized", InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate(objectID, name, 1024)
	require.NoError(t, err)
	media, err := acquisitiondomain.NewAcquiredMedia(request, release, candidate)
	require.NoError(t, err)
	return media
}
