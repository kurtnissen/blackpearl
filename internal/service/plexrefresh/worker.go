// Package plexrefresh coordinates best-effort Plex library scans after catalog publication.
package plexrefresh

import (
	"context"
	"errors"
	"time"
)

// Refresher requests one scan of all configured BlackPearl Plex libraries.
type Refresher interface {
	Refresh(ctx context.Context) error
}

// Options configures publication coalescing and failure retries.
type Options struct {
	Debounce      time.Duration
	RetryInterval time.Duration
	OnError       func(error)
}

// Worker turns nonblocking publication signals into serialized Plex refreshes.
type Worker struct {
	refresher     Refresher
	debounce      time.Duration
	retryInterval time.Duration
	onError       func(error)
	notifications chan struct{}
}

// New constructs one process-lifetime refresh worker.
func New(refresher Refresher, options Options) (*Worker, error) {
	if refresher == nil {
		return nil, errors.New("Plex refresher is required")
	}
	if options.Debounce <= 0 || options.RetryInterval <= 0 {
		return nil, errors.New("Plex refresh debounce and retry interval must be positive")
	}
	onError := options.OnError
	if onError == nil {
		onError = func(error) {}
	}
	return &Worker{
		refresher: refresher, debounce: options.Debounce, retryInterval: options.RetryInterval,
		onError: onError, notifications: make(chan struct{}, 1),
	}, nil
}

// Notify records that a successfully published namespace needs a Plex scan.
// It never blocks the publication transaction.
func (w *Worker) Notify() {
	select {
	case w.notifications <- struct{}{}:
	default:
	}
}

// Run processes publication signals until context cancellation.
func (w *Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.notifications:
		}
		if !wait(ctx, w.debounce) {
			return
		}
		for {
			w.drainNotifications()
			err := w.refresher.Refresh(ctx)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			w.onError(err)
			if !wait(ctx, w.retryInterval) {
				return
			}
		}
	}
}

func (w *Worker) drainNotifications() {
	for {
		select {
		case <-w.notifications:
		default:
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
