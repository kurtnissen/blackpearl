package watchlist

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
	"go.opentelemetry.io/otel"
)

const durableCompletionTimeout = 5 * time.Second

// AcquisitionQueue owns versioned claims and durable outcomes.
type AcquisitionQueue interface {
	Claim(ctx context.Context, now time.Time, leaseDuration time.Duration) (acquisitiondomain.WatchlistClaim, error)
	Complete(ctx context.Context, claim acquisitiondomain.WatchlistClaim, completion acquisitiondomain.WatchlistCompletion) error
}

// MovieAcquirer publishes one validated cached-only movie request.
type MovieAcquirer interface {
	Acquire(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error)
}

// WorkerOptions bounds serialized automatic acquisition and retry behavior.
type WorkerOptions struct {
	LeaseDuration      time.Duration
	AcquisitionTimeout time.Duration
	IdleInterval       time.Duration
	NotCachedCooldown  time.Duration
	RetryCooldown      time.Duration
	Now                func() time.Time
}

// Worker serially converts eligible movie observations into cached-only
// acquisition attempts.
type Worker struct {
	queue    AcquisitionQueue
	acquirer MovieAcquirer
	options  WorkerOptions
	now      func() time.Time

	processMu sync.Mutex
}

// NewWorker constructs a serialized watchlist acquisition worker.
func NewWorker(queue AcquisitionQueue, acquirer MovieAcquirer, options WorkerOptions) (*Worker, error) {
	if queue == nil || acquirer == nil {
		return nil, errors.New("watchlist worker dependencies are required")
	}
	for name, value := range map[string]time.Duration{
		"lease duration":      options.LeaseDuration,
		"acquisition timeout": options.AcquisitionTimeout,
		"idle interval":       options.IdleInterval,
		"not-cached cooldown": options.NotCachedCooldown,
		"retry cooldown":      options.RetryCooldown,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("watchlist worker %s must be positive", name)
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Worker{queue: queue, acquirer: acquirer, options: options, now: now}, nil
}

// ProcessOne claims and resolves at most one eligible movie.
func (w *Worker) ProcessOne(ctx context.Context) (acquisitiondomain.WatchlistQueueState, error) {
	w.processMu.Lock()
	defer w.processMu.Unlock()
	ctx, span := otel.Tracer("blackpearl/watchlist").Start(ctx, "watchlist.process_one")
	defer span.End()
	now := w.now().UTC()
	if now.IsZero() {
		return "", ErrUnavailable
	}
	claim, err := w.queue.Claim(ctx, now, w.options.LeaseDuration)
	if err != nil {
		return "", workerBoundaryError(ctx, "claim watchlist movie", err)
	}
	request, err := claim.Item().SearchRequest()
	if err != nil {
		return w.completeDurably(ctx, claim, acquisitiondomain.NewWatchlistManualReview())
	}
	acquisitionContext, cancel := context.WithTimeout(ctx, w.options.AcquisitionTimeout)
	media, acquireErr := w.acquirer.Acquire(acquisitionContext, request)
	cancel()
	if acquireErr == nil {
		completion, completionErr := acquisitiondomain.NewWatchlistSucceeded(media.Candidate().ObjectID)
		if completionErr != nil {
			return w.completeDurably(ctx, claim, acquisitiondomain.NewWatchlistManualReview())
		}
		return w.completeDurably(ctx, claim, completion)
	}
	completion, shouldComplete, completionErr := w.classify(acquireErr, now)
	if completionErr != nil {
		return "", completionErr
	}
	if !shouldComplete {
		return "", workerBoundaryError(ctx, "acquire watchlist movie", acquireErr)
	}
	return w.completeDurably(ctx, claim, completion)
}

func (w *Worker) classify(err error, now time.Time) (acquisitiondomain.WatchlistCompletion, bool, error) {
	switch {
	case errors.Is(err, acquisitionservice.ErrAmbiguousMutation), errors.Is(err, acquisitionservice.ErrNoPlayableMedia):
		return acquisitiondomain.NewWatchlistManualReview(), true, nil
	case errors.Is(err, acquisitionservice.ErrNotCached):
		completion, completionErr := acquisitiondomain.NewWatchlistDeferred(
			acquisitiondomain.WatchlistQueueStateNotCached, now.Add(w.options.NotCachedCooldown),
		)
		return completion, true, completionErr
	case errors.Is(err, domain.ErrUnauthorized), errors.Is(err, domain.ErrNotConfigured), errors.Is(err, acquisitionservice.ErrUnavailable):
		completion, completionErr := acquisitiondomain.NewWatchlistDeferred(
			acquisitiondomain.WatchlistQueueStateRetryable, now.Add(w.options.RetryCooldown),
		)
		return completion, true, completionErr
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return acquisitiondomain.WatchlistCompletion{}, false, nil
	default:
		return acquisitiondomain.NewWatchlistManualReview(), true, nil
	}
}

func (w *Worker) completeDurably(
	ctx context.Context,
	claim acquisitiondomain.WatchlistClaim,
	completion acquisitiondomain.WatchlistCompletion,
) (acquisitiondomain.WatchlistQueueState, error) {
	completionContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), durableCompletionTimeout)
	defer cancel()
	if err := w.queue.Complete(completionContext, claim, completion); err != nil {
		return "", workerBoundaryError(completionContext, "complete watchlist movie", err)
	}
	return completion.State(), nil
}

// Run drains eligible movies one at a time and waits when no work is ready.
func (w *Worker) Run(ctx context.Context) {
	for {
		_, err := w.ProcessOne(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(w.options.IdleInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func workerBoundaryError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	if errors.Is(err, acquisitiondomain.ErrStaleWatchlistClaim) {
		return fmt.Errorf("%s: %w", operation, acquisitiondomain.ErrStaleWatchlistClaim)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
