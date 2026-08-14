package watchlist_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	watchlistservice "github.com/blackpearl-media/blackpearl/internal/service/watchlist"
	"github.com/stretchr/testify/require"
)

func TestObserverSyncPersistsSnapshotAndReturnsAggregateStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 14, 14, 0, 0, 0, time.UTC)
	items := []acquisitiondomain.WatchlistItem{mustObserverItem(t, "plex://movie/one")}
	gateway := &fakeSnapshotGateway{items: items}
	queue := &fakeQueue{status: acquisitiondomain.WatchlistQueueStatus{PendingMovies: 1, ObservedShows: 2}}
	observer := newObserver(t, gateway, queue, time.Hour, func() time.Time { return now })

	require.NoError(t, observer.Sync(context.Background()))
	status, err := observer.Status(context.Background())

	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.False(t, status.AcquisitionEnabled)
	require.True(t, status.Healthy)
	require.NotNil(t, status.LastSyncAt)
	require.Equal(t, now, *status.LastSyncAt)
	require.Equal(t, queue.status, status.Queue)
	require.Equal(t, items, queue.items)
	require.Equal(t, now, queue.observedAt)
}

func TestObserverStatusReportsAutomaticAcquisitionPolicy(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{acquisitionEnabled: true}
	observer, err := watchlistservice.NewObserver(&fakeSnapshotGateway{}, queue, watchlistservice.ObserverOptions{
		PollInterval: time.Hour,
	})
	require.NoError(t, err)

	status, err := observer.Status(context.Background())

	require.NoError(t, err)
	require.True(t, status.AcquisitionEnabled)
}

func TestObserverMakesOnlyPostBaselineItemsEligibleForAutomaticAcquisition(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{acquisitionEnabled: true}
	observer, err := watchlistservice.NewObserver(&fakeSnapshotGateway{}, queue, watchlistservice.ObserverOptions{
		PollInterval: time.Hour,
	})
	require.NoError(t, err)

	require.NoError(t, observer.Sync(context.Background()))
	require.NoError(t, observer.Sync(context.Background()))

	require.Equal(t, []bool{false, true}, queue.autoEligibility)
}

func TestObserverChangesDurableAutomaticAcquisitionPolicyAtRuntime(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	observer := newObserver(t, &fakeSnapshotGateway{}, queue, time.Hour, time.Now)
	require.NoError(t, observer.Sync(context.Background()))

	require.NoError(t, observer.SetAcquisitionEnabled(context.Background(), true))
	require.NoError(t, observer.Sync(context.Background()))
	status, err := observer.Status(context.Background())

	require.NoError(t, err)
	require.True(t, status.AcquisitionEnabled)
	require.Equal(t, []bool{false, true}, queue.autoEligibility)
}

func TestObserverSyncSanitizesProviderAndRepositoryFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		gatewayErr error
		queueErr   error
		want       error
	}{
		{name: "provider unauthorized", gatewayErr: domain.ErrUnauthorized, want: domain.ErrUnauthorized},
		{name: "provider unavailable", gatewayErr: errors.New("private watchlist response"), want: watchlistservice.ErrUnavailable},
		{name: "queue unavailable", queueErr: errors.New("private database path"), want: watchlistservice.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gateway := &fakeSnapshotGateway{items: []acquisitiondomain.WatchlistItem{mustObserverItem(t, "plex://movie/one")}, err: test.gatewayErr}
			queue := &fakeQueue{upsertErr: test.queueErr}
			observer := newObserver(t, gateway, queue, time.Hour, time.Now)

			err := observer.Sync(context.Background())

			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "private")
			status, statusErr := observer.Status(context.Background())
			require.NoError(t, statusErr)
			require.False(t, status.Healthy)
			require.Nil(t, status.LastSyncAt)
		})
	}
}

func TestObserverRunPollsImmediatelySeriallyAndStopsWithContext(t *testing.T) {
	t.Parallel()
	gateway := &fakeSnapshotGateway{items: []acquisitiondomain.WatchlistItem{mustObserverItem(t, "plex://movie/run")}}
	queue := &fakeQueue{}
	observer := newObserver(t, gateway, queue, 5*time.Millisecond, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observer.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return gateway.calls.Load() >= 2 }, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not stop after cancellation")
	}
	require.Equal(t, int32(1), gateway.maximumConcurrent.Load())
}

func TestObserverStatusHonorsCancellationAndSanitizesQueueFailure(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{statusErr: errors.New("private sqlite detail")}
	observer := newObserver(t, &fakeSnapshotGateway{}, queue, time.Hour, time.Now)

	_, err := observer.Status(context.Background())
	require.ErrorIs(t, err, watchlistservice.ErrUnavailable)
	require.NotContains(t, err.Error(), "private")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = observer.Status(ctx)
	require.ErrorIs(t, err, context.Canceled)
	err = observer.Sync(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewObserverRequiresDependenciesAndPositiveInterval(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	gateway := &fakeSnapshotGateway{}

	_, err := watchlistservice.NewObserver(nil, queue, watchlistservice.ObserverOptions{PollInterval: time.Minute})
	require.Error(t, err)
	_, err = watchlistservice.NewObserver(gateway, nil, watchlistservice.ObserverOptions{PollInterval: time.Minute})
	require.Error(t, err)
	_, err = watchlistservice.NewObserver(gateway, queue, watchlistservice.ObserverOptions{})
	require.Error(t, err)
}

func newObserver(
	t *testing.T,
	gateway *fakeSnapshotGateway,
	queue *fakeQueue,
	interval time.Duration,
	now func() time.Time,
) *watchlistservice.Observer {
	t.Helper()
	observer, err := watchlistservice.NewObserver(gateway, queue, watchlistservice.ObserverOptions{PollInterval: interval, Now: now})
	require.NoError(t, err)
	return observer
}

func mustObserverItem(t *testing.T, externalID string) acquisitiondomain.WatchlistItem {
	t.Helper()
	item, err := acquisitiondomain.NewWatchlistItem(acquisitiondomain.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: externalID, MediaType: acquisitiondomain.WatchlistMediaTypeMovie,
		Title: "Example", Year: 2026,
	})
	require.NoError(t, err)
	return item
}

type fakeSnapshotGateway struct {
	items             []acquisitiondomain.WatchlistItem
	err               error
	calls             atomic.Int32
	concurrent        atomic.Int32
	maximumConcurrent atomic.Int32
}

func (f *fakeSnapshotGateway) Snapshot(context.Context) ([]acquisitiondomain.WatchlistItem, error) {
	current := f.concurrent.Add(1)
	defer f.concurrent.Add(-1)
	f.calls.Add(1)
	for {
		maximum := f.maximumConcurrent.Load()
		if current <= maximum || f.maximumConcurrent.CompareAndSwap(maximum, current) {
			break
		}
	}
	return append([]acquisitiondomain.WatchlistItem(nil), f.items...), f.err
}

type fakeQueue struct {
	mu                 sync.Mutex
	items              []acquisitiondomain.WatchlistItem
	observedAt         time.Time
	upsertErr          error
	status             acquisitiondomain.WatchlistQueueStatus
	statusErr          error
	autoEligibility    []bool
	acquisitionEnabled bool
	policyErr          error
}

func (f *fakeQueue) UpsertSnapshot(_ context.Context, items []acquisitiondomain.WatchlistItem, observedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append([]acquisitiondomain.WatchlistItem(nil), items...)
	f.observedAt = observedAt
	return f.upsertErr
}

func (f *fakeQueue) UpsertSnapshotPolicy(
	_ context.Context,
	items []acquisitiondomain.WatchlistItem,
	observedAt time.Time,
	autoEligible bool,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append([]acquisitiondomain.WatchlistItem(nil), items...)
	f.observedAt = observedAt
	f.autoEligibility = append(f.autoEligibility, autoEligible)
	return f.upsertErr
}

func (f *fakeQueue) Status(context.Context) (acquisitiondomain.WatchlistQueueStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeQueue) AcquisitionEnabled(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquisitionEnabled, f.policyErr
}

func (f *fakeQueue) SetAcquisitionEnabled(_ context.Context, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.policyErr != nil {
		return f.policyErr
	}
	f.acquisitionEnabled = enabled
	return nil
}
