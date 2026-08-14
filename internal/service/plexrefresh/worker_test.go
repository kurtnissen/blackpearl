package plexrefresh_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	plexrefresh "github.com/kurtnissen/blackpearl/internal/service/plexrefresh"
	"github.com/stretchr/testify/require"
)

type scriptedRefresher struct {
	mu      sync.Mutex
	results []error
	calls   chan context.Context
}

func (f *scriptedRefresher) Refresh(ctx context.Context) error {
	f.calls <- ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

func TestWorkerCoalescesBurstIntoOneRefresh(t *testing.T) {
	t.Parallel()
	refresher := &scriptedRefresher{calls: make(chan context.Context, 10)}
	worker, err := plexrefresh.New(refresher, plexrefresh.Options{
		Debounce: 20 * time.Millisecond, RetryInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()

	for range 1_000 {
		worker.Notify()
	}

	select {
	case <-refresher.calls:
	case <-time.After(time.Second):
		t.Fatal("worker did not refresh")
	}
	select {
	case <-refresher.calls:
		t.Fatal("burst produced more than one refresh")
	case <-time.After(60 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerRetriesFailureWithoutAnotherNotification(t *testing.T) {
	t.Parallel()
	refreshErr := errors.New("Plex unavailable")
	refresher := &scriptedRefresher{results: []error{refreshErr, nil}, calls: make(chan context.Context, 10)}
	reported := make(chan error, 1)
	worker, err := plexrefresh.New(refresher, plexrefresh.Options{
		Debounce: 5 * time.Millisecond, RetryInterval: 10 * time.Millisecond,
		OnError: func(err error) { reported <- err },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go worker.Run(ctx)

	worker.Notify()

	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-refresher.calls:
		case <-time.After(time.Second):
			t.Fatalf("refresh attempt %d did not occur", attempt+1)
		}
	}
	select {
	case err := <-reported:
		require.ErrorIs(t, err, refreshErr)
	case <-time.After(time.Second):
		t.Fatal("refresh failure was not reported")
	}
	select {
	case <-refresher.calls:
		t.Fatal("worker kept retrying after success")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestWorkerCoalescesNotificationReceivedDuringRetry(t *testing.T) {
	t.Parallel()
	refreshErr := errors.New("Plex starting")
	refresher := &scriptedRefresher{results: []error{refreshErr, nil}, calls: make(chan context.Context, 10)}
	failure := make(chan error, 1)
	worker, err := plexrefresh.New(refresher, plexrefresh.Options{
		Debounce: time.Millisecond, RetryInterval: 30 * time.Millisecond,
		OnError: func(err error) { failure <- err },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go worker.Run(ctx)
	worker.Notify()
	select {
	case <-refresher.calls:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not occur")
	}
	select {
	case <-failure:
	case <-time.After(time.Second):
		t.Fatal("initial failure was not observed")
	}
	for range 100 {
		worker.Notify()
	}

	select {
	case <-refresher.calls:
	case <-time.After(time.Second):
		t.Fatal("retry did not occur")
	}
	select {
	case <-refresher.calls:
		t.Fatal("retry-period notifications produced a redundant scan")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestWorkerCancellationStopsInflightRefresh(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	refresher := blockingRefresher{started: started}
	worker, err := plexrefresh.New(refresher, plexrefresh.Options{
		Debounce: time.Millisecond, RetryInterval: time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	worker.Notify()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel in-flight refresh")
	}
}

type blockingRefresher struct {
	started chan<- struct{}
}

func (f blockingRefresher) Refresh(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestNewWorkerRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	tests := []plexrefresh.Options{
		{},
		{Debounce: time.Second},
		{Debounce: -time.Second, RetryInterval: time.Second},
	}
	for _, options := range tests {
		_, err := plexrefresh.New(&scriptedRefresher{calls: make(chan context.Context, 1)}, options)
		require.Error(t, err)
	}
	_, err := plexrefresh.New(nil, plexrefresh.Options{Debounce: time.Second, RetryInterval: time.Second})
	require.Error(t, err)
}
