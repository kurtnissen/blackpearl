package acquisition_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	acquisitionservice "github.com/kurtnissen/blackpearl/internal/service/acquisition"
	"github.com/stretchr/testify/require"
)

func TestServiceAcquirePublishesExactEpisodeAfterBoundedReadinessPoll(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewEpisodeSearch("Example Show", 2026, 7, 2)
	require.NoError(t, err)
	first := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	second := mustRelease(t, "second", "abcdef0123456789abcdef0123456789abcdef01")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{second, first}}
	gateway := &fakeCachedGateway{
		cached: []acquisitiondomain.Release{second}, created: created,
		inspections: []inspectionResult{
			{err: acquisitiondomain.ErrNotReady},
			{items: []domain.MediaCandidate{
				mustCandidate(t, "17:1", "Example.Show.S07E01.mkv", 30),
				mustCandidate(t, "17:2", "Example.Show.S07E02.1080p.mkv", 20),
			}},
		},
	}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 3)

	result, err := service.Acquire(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, second, gateway.createdRelease)
	require.Equal(t, 2, gateway.inspectCalls)
	require.Equal(t, "17:2", result.Candidate().ObjectID)
	require.Equal(t, []acquisitiondomain.AcquiredMedia{result}, publisher.published)
}

func TestServiceAcquireDoesNotMutateWhenNoRankedReleaseIsCached(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{release}}
	gateway := &fakeCachedGateway{}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 2)

	_, err = service.Acquire(context.Background(), request)

	require.ErrorIs(t, err, acquisitionservice.ErrNotCached)
	require.Zero(t, gateway.createCalls)
	require.Zero(t, gateway.inspectCalls)
	require.Empty(t, publisher.published)
}

func TestServiceAcquireDoesNotFallThroughToLowerRankedCachedRelease(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Sintel", 2010)
	require.NoError(t, err)
	preferred := mustRelease(t, "full-film", "0123456789abcdef0123456789abcdef01234567")
	preview := mustRelease(t, "preview", "abcdef0123456789abcdef0123456789abcdef01")
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{preferred, preview}}
	gateway := &fakeCachedGateway{cached: []acquisitiondomain.Release{preview}}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 1)

	_, err = service.Acquire(context.Background(), request)

	require.ErrorIs(t, err, acquisitionservice.ErrNotCached)
	require.Equal(t, []acquisitiondomain.Release{preferred}, gateway.cacheQuery)
	require.Zero(t, gateway.createCalls)
	require.Empty(t, publisher.published)
}

func TestServiceAcquireNeverFallsThroughAfterAmbiguousCreateFailure(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	first := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	second := mustRelease(t, "second", "abcdef0123456789abcdef0123456789abcdef01")
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{first, second}}
	gateway := &fakeCachedGateway{cached: []acquisitiondomain.Release{first, second}, createErr: errors.New("ambiguous secret magnet")}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 2)

	_, err = service.Acquire(context.Background(), request)

	require.ErrorIs(t, err, acquisitionservice.ErrAmbiguousMutation)
	require.NotContains(t, err.Error(), "secret")
	require.NotContains(t, err.Error(), "magnet")
	require.Equal(t, 1, gateway.createCalls)
	require.Equal(t, first, gateway.createdRelease)
	require.Zero(t, gateway.inspectCalls)
	require.Empty(t, publisher.published)
}

func TestServiceAcquireBoundsReadinessPollingAndDoesNotPublish(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{release}}
	gateway := &fakeCachedGateway{cached: []acquisitiondomain.Release{release}, created: created, inspectErr: acquisitiondomain.ErrNotReady}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 3)

	_, err = service.Acquire(context.Background(), request)

	require.ErrorIs(t, err, acquisitionservice.ErrUnavailable)
	require.Equal(t, 3, gateway.inspectCalls)
	require.Empty(t, publisher.published)
}

func TestServiceAcquireSelectsBestMovieCandidateDeterministically(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{release}}
	gateway := &fakeCachedGateway{
		cached: []acquisitiondomain.Release{release}, created: created,
		inspections: []inspectionResult{{items: []domain.MediaCandidate{
			mustCandidate(t, "17:1", "Unrelated.Feature.mkv", 100),
			mustCandidate(t, "17:2", "Example.Movie.2026.720p.mkv", 20),
			mustCandidate(t, "17:3", "Example.Movie.2026.1080p.mkv", 30),
		}}},
	}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 1)

	result, err := service.Acquire(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, "17:3", result.Candidate().ObjectID)
}

func TestServiceAcquireRejectsReadyObjectWithoutMatchingEpisode(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewEpisodeSearch("Example Show", 2026, 7, 2)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{release}}
	gateway := &fakeCachedGateway{
		cached: []acquisitiondomain.Release{release}, created: created,
		inspections: []inspectionResult{{items: []domain.MediaCandidate{mustCandidate(t, "17:1", "Example.Show.S07E01.mkv", 20)}}},
	}
	publisher := &fakePublisher{}
	service := newService(t, searcher, gateway, publisher, 1)

	_, err = service.Acquire(context.Background(), request)

	require.ErrorIs(t, err, acquisitionservice.ErrNoPlayableMedia)
	require.Empty(t, publisher.published)
}

func TestServiceAcquirePreservesAuthorizationButSanitizesOtherBoundaries(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)

	tests := []struct {
		name      string
		searcher  *fakeSearcher
		gateway   *fakeCachedGateway
		publisher *fakePublisher
		want      error
	}{
		{name: "search auth", searcher: &fakeSearcher{err: domain.ErrUnauthorized}, gateway: &fakeCachedGateway{}, publisher: &fakePublisher{}, want: domain.ErrUnauthorized},
		{name: "cache failure", searcher: &fakeSearcher{releases: []acquisitiondomain.Release{release}}, gateway: &fakeCachedGateway{cachedErr: errors.New("secret locator")}, publisher: &fakePublisher{}, want: acquisitionservice.ErrUnavailable},
		{name: "inspect auth", searcher: &fakeSearcher{releases: []acquisitiondomain.Release{release}}, gateway: &fakeCachedGateway{cached: []acquisitiondomain.Release{release}, created: created, inspectErr: domain.ErrUnauthorized}, publisher: &fakePublisher{}, want: domain.ErrUnauthorized},
		{name: "publish failure", searcher: &fakeSearcher{releases: []acquisitiondomain.Release{release}}, gateway: &fakeCachedGateway{cached: []acquisitiondomain.Release{release}, created: created, inspections: []inspectionResult{{items: []domain.MediaCandidate{mustCandidate(t, "17:1", "Example.2026.mkv", 20)}}}}, publisher: &fakePublisher{err: errors.New("private persistence path")}, want: acquisitionservice.ErrAmbiguousMutation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newService(t, test.searcher, test.gateway, test.publisher, 1)

			_, err := service.Acquire(context.Background(), request)

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "secret")
			require.NotContains(t, err.Error(), "locator")
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, err.Error(), "persistence")
		})
	}
}

func TestServiceAcquireHonorsCancellationDuringReadinessWait(t *testing.T) {
	t.Parallel()
	request, err := acquisitiondomain.NewMovieSearch("Example", 2026)
	require.NoError(t, err)
	release := mustRelease(t, "first", "0123456789abcdef0123456789abcdef01234567")
	created, err := acquisitiondomain.NewCreatedObject("torbox-torrent", "17")
	require.NoError(t, err)
	searcher := &fakeSearcher{releases: []acquisitiondomain.Release{release}}
	inspectStarted := make(chan struct{})
	gateway := &fakeCachedGateway{
		cached: []acquisitiondomain.Release{release}, created: created,
		inspectErr: acquisitiondomain.ErrNotReady, inspectStarted: inspectStarted,
	}
	publisher := &fakePublisher{}
	service, err := acquisitionservice.New(searcher, gateway, publisher, acquisitionservice.Options{InspectionAttempts: 3, InspectionInterval: time.Hour})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := service.Acquire(ctx, request)
		result <- acquireErr
	}()
	<-inspectStarted
	cancel()

	err = <-result

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, publisher.published)
}

func newService(t *testing.T, searcher *fakeSearcher, gateway *fakeCachedGateway, publisher *fakePublisher, attempts int) *acquisitionservice.Service {
	t.Helper()
	service, err := acquisitionservice.New(searcher, gateway, publisher, acquisitionservice.Options{
		InspectionAttempts: attempts,
		InspectionInterval: time.Nanosecond,
	})
	require.NoError(t, err)
	return service
}

func mustRelease(t *testing.T, sourceID string, infoHash string) acquisitiondomain.Release {
	t.Helper()
	release, err := acquisitiondomain.NewRelease(acquisitiondomain.ReleaseInput{
		Provider: "prowlarr", SourceID: sourceID, Title: "Example.Movie.2026", Protocol: acquisitiondomain.ReleaseProtocolTorrent,
		Size: 20, Indexer: "authorized", InfoHash: infoHash,
	})
	require.NoError(t, err)
	return release
}

func mustCandidate(t *testing.T, objectID string, name string, size int64) domain.MediaCandidate {
	t.Helper()
	candidate, err := domain.NewMediaCandidate(objectID, name, size)
	require.NoError(t, err)
	return candidate
}

type fakeSearcher struct {
	releases []acquisitiondomain.Release
	err      error
}

func (f *fakeSearcher) Search(context.Context, acquisitiondomain.SearchRequest) ([]acquisitiondomain.Release, error) {
	return append([]acquisitiondomain.Release(nil), f.releases...), f.err
}

type inspectionResult struct {
	items []domain.MediaCandidate
	err   error
}

type fakeCachedGateway struct {
	cached         []acquisitiondomain.Release
	cacheQuery     []acquisitiondomain.Release
	cachedErr      error
	created        acquisitiondomain.CreatedObject
	createErr      error
	inspectErr     error
	inspections    []inspectionResult
	createCalls    int
	inspectCalls   int
	createdRelease acquisitiondomain.Release
	inspectStarted chan struct{}
	inspectOnce    sync.Once
}

func (f *fakeCachedGateway) CachedTorrents(_ context.Context, releases []acquisitiondomain.Release) ([]acquisitiondomain.Release, error) {
	f.cacheQuery = append([]acquisitiondomain.Release(nil), releases...)
	return append([]acquisitiondomain.Release(nil), f.cached...), f.cachedErr
}

func (f *fakeCachedGateway) CreateCachedTorrent(_ context.Context, release acquisitiondomain.Release) (acquisitiondomain.CreatedObject, error) {
	f.createCalls++
	f.createdRelease = release
	return f.created, f.createErr
}

func (f *fakeCachedGateway) InspectCreatedTorrent(context.Context, acquisitiondomain.CreatedObject) (acquisitiondomain.PreparationInspection, error) {
	f.inspectCalls++
	if f.inspectStarted != nil {
		f.inspectOnce.Do(func() { close(f.inspectStarted) })
	}
	if len(f.inspections) > 0 {
		index := f.inspectCalls - 1
		if index >= len(f.inspections) {
			index = len(f.inspections) - 1
		}
		result := f.inspections[index]
		inspection, err := acquisitiondomain.NewPreparationInspection(result.items, 100)
		if err != nil {
			return acquisitiondomain.PreparationInspection{}, err
		}
		return inspection, result.err
	}
	inspection, err := acquisitiondomain.NewPreparationInspection(nil, 0)
	if err != nil {
		return acquisitiondomain.PreparationInspection{}, err
	}
	return inspection, f.inspectErr
}

type fakePublisher struct {
	published []acquisitiondomain.AcquiredMedia
	err       error
}

func (f *fakePublisher) PublishAcquired(_ context.Context, media acquisitiondomain.AcquiredMedia) error {
	f.published = append(f.published, media)
	return f.err
}
