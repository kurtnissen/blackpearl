// Package playbackadvance coordinates exact, playback-driven episode intents.
package playbackadvance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const (
	watchlistSource          = "plex-watchlist"
	minimumPlaybackOffset    = 120 * time.Second
	minimumPlaybackPercent   = 10
	observationFreshnessPoll = 2
)

// ErrUnavailable is a public-safe playback advancement boundary failure.
var ErrUnavailable = errors.New("playback advancement is temporarily unavailable")

// PlaybackSnapshotter reads bounded active episode playback evidence.
type PlaybackSnapshotter interface {
	Snapshot(ctx context.Context) ([]domain.EpisodePlayback, error)
}

// PublishedEpisodeIndex resolves an exact BlackPearl virtual episode path.
type PublishedEpisodeIndex interface {
	FindPublishedEpisode(ctx context.Context, virtualPath string) (domain.SetupConfiguration, bool, error)
}

// EpisodeFrontier owns the durable optimistic Watchlist episode transition.
type EpisodeFrontier interface {
	CanAdvanceEpisode(
		ctx context.Context,
		source string,
		externalID string,
		objectID string,
		current domain.EpisodeCoordinate,
		observedAfter time.Time,
	) (bool, error)
	AdvanceEpisode(
		ctx context.Context,
		source string,
		externalID string,
		objectID string,
		current domain.EpisodeCoordinate,
		next domain.EpisodeCoordinate,
		observedAfter time.Time,
		now time.Time,
	) (bool, error)
}

// NextEpisodeResolver resolves one exact metadata successor.
type NextEpisodeResolver interface {
	Next(
		ctx context.Context,
		externalShowID string,
		current domain.EpisodeCoordinate,
	) (domain.EpisodeCoordinate, error)
}

// WorkerOptions bounds polling, external I/O, and Watchlist freshness.
type WorkerOptions struct {
	PollInterval          time.Duration
	OperationTimeout      time.Duration
	WatchlistPollInterval time.Duration
	Now                   func() time.Time
}

// Worker serially advances a succeeded show frontier by at most one exact
// metadata-resolved episode per qualifying published playback observation.
type Worker struct {
	snapshotter PlaybackSnapshotter
	index       PublishedEpisodeIndex
	frontier    EpisodeFrontier
	resolver    NextEpisodeResolver
	options     WorkerOptions
	now         func() time.Time

	processMu sync.Mutex
}

// NewWorker constructs a serialized playback advancement worker.
func NewWorker(
	snapshotter PlaybackSnapshotter,
	index PublishedEpisodeIndex,
	frontier EpisodeFrontier,
	resolver NextEpisodeResolver,
	options WorkerOptions,
) (*Worker, error) {
	if snapshotter == nil || index == nil || frontier == nil || resolver == nil {
		return nil, errors.New("playback advancement dependencies are required")
	}
	for name, value := range map[string]time.Duration{
		"poll interval":           options.PollInterval,
		"operation timeout":       options.OperationTimeout,
		"Watchlist poll interval": options.WatchlistPollInterval,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("playback advancement %s must be positive", name)
		}
	}
	if options.WatchlistPollInterval > time.Duration(math.MaxInt64/observationFreshnessPoll) {
		return nil, errors.New("playback advancement Watchlist poll interval is too large")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Worker{
		snapshotter: snapshotter, index: index, frontier: frontier, resolver: resolver,
		options: options, now: now,
	}, nil
}

// Process reads one playback snapshot and atomically advances every distinct
// exact episode frontier that still satisfies all durable gates.
func (w *Worker) Process(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("process playback advancement: %w", err)
	}
	w.processMu.Lock()
	defer w.processMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("process playback advancement: %w", err)
	}
	ctx, span := otel.Tracer("blackpearl/playbackadvance").Start(ctx, "playback_advance.process")
	defer span.End()
	operationContext, cancel := context.WithTimeout(ctx, w.options.OperationTimeout)
	defer cancel()
	return w.process(operationContext)
}

func (w *Worker) process(ctx context.Context) (int, error) {
	now := w.now().UTC()
	if now.IsZero() {
		return 0, fmt.Errorf("read playback advancement clock: %w", ErrUnavailable)
	}
	playbacks, err := w.snapshotter.Snapshot(ctx)
	if err != nil {
		return 0, boundaryError(ctx, "read Plex playback", err)
	}
	observedAfter := now.Add(-observationFreshnessPoll * w.options.WatchlistPollInterval)
	advancedCount := 0
	for _, playback := range playbacks {
		if !playback.Qualifies(minimumPlaybackOffset, minimumPlaybackPercent) {
			continue
		}
		configuration, found, findErr := w.index.FindPublishedEpisode(ctx, playback.VirtualPath())
		if findErr != nil {
			return advancedCount, boundaryError(ctx, "resolve published playback episode", findErr)
		}
		if !found || configuration.MediaType != domain.MediaTypeEpisode ||
			configuration.Season != playback.Coordinate().Season() ||
			configuration.Episode != playback.Coordinate().Episode() {
			continue
		}
		backing := configuration.Backing()
		eligible, frontierErr := w.frontier.CanAdvanceEpisode(
			ctx, watchlistSource, playback.ExternalShowID(), backing.ObjectID,
			playback.Coordinate(), observedAfter,
		)
		if frontierErr != nil {
			return advancedCount, boundaryError(ctx, "check episode frontier", frontierErr)
		}
		if !eligible {
			continue
		}
		next, nextErr := w.resolver.Next(ctx, playback.ExternalShowID(), playback.Coordinate())
		if errors.Is(nextErr, domain.ErrNotFound) {
			continue
		}
		if nextErr != nil {
			return advancedCount, boundaryError(ctx, "resolve next Plex episode", nextErr)
		}
		advanced, advanceErr := w.frontier.AdvanceEpisode(
			ctx, watchlistSource, playback.ExternalShowID(), backing.ObjectID,
			playback.Coordinate(), next, observedAfter, now,
		)
		if advanceErr != nil {
			return advancedCount, boundaryError(ctx, "advance episode frontier", advanceErr)
		}
		if advanced {
			advancedCount++
		}
	}
	return advancedCount, nil
}

// Run polls playback at a fixed interval until cancellation. Individual
// failures are retried on the next interval and never stop the service.
func (w *Worker) Run(ctx context.Context) {
	for {
		if _, err := w.Process(ctx); err != nil && ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(w.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func boundaryError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
