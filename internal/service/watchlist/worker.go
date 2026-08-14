package watchlist

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const durableCompletionTimeout = 5 * time.Second

// AcquisitionQueue owns versioned claims and durable outcomes.
type AcquisitionQueue interface {
	Claim(ctx context.Context, now time.Time, leaseDuration time.Duration) (acquisition.WatchlistClaim, error)
	AttachJob(ctx context.Context, claim acquisition.WatchlistClaim, jobID string, nextAttempt time.Time, now time.Time) error
	DeferJob(ctx context.Context, claim acquisition.WatchlistClaim, nextAttempt time.Time, now time.Time) error
	Complete(ctx context.Context, claim acquisition.WatchlistClaim, completion acquisition.WatchlistCompletion, now time.Time) error
}

// JobManager submits and reads restart-safe background acquisition jobs.
type JobManager interface {
	Submit(ctx context.Context, request acquisition.SearchRequest) (acquisition.AcquisitionJob, bool, error)
	Get(ctx context.Context, id string) (acquisition.AcquisitionJob, error)
}

// PublishedMediaIndex finds exact intent already exposed through the active
// Plex manifest. It prevents duplicate provider mutations for published media.
type PublishedMediaIndex interface {
	FindPublished(ctx context.Context, request acquisition.SearchRequest) (objectID string, found bool, err error)
}

// WorkerOptions bounds serialized automatic acquisition and retry behavior.
type WorkerOptions struct {
	LeaseDuration     time.Duration
	OperationTimeout  time.Duration
	IdleInterval      time.Duration
	ReconcileInterval time.Duration
	NotCachedCooldown time.Duration
	RetryCooldown     time.Duration
	Now               func() time.Time
}

// Worker serially converts eligible movie observations into cached-only
// acquisition attempts.
type Worker struct {
	queue   AcquisitionQueue
	manager JobManager
	index   PublishedMediaIndex
	options WorkerOptions
	now     func() time.Time

	processMu sync.Mutex
}

// NewWorker constructs a serialized watchlist-to-background-job worker.
func NewWorker(queue AcquisitionQueue, manager JobManager, index PublishedMediaIndex, options WorkerOptions) (*Worker, error) {
	if queue == nil || manager == nil || index == nil {
		return nil, errors.New("watchlist worker dependencies are required")
	}
	for name, value := range map[string]time.Duration{
		"lease duration":      options.LeaseDuration,
		"operation timeout":   options.OperationTimeout,
		"idle interval":       options.IdleInterval,
		"reconcile interval":  options.ReconcileInterval,
		"not-cached cooldown": options.NotCachedCooldown,
		"retry cooldown":      options.RetryCooldown,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("watchlist worker %s must be positive", name)
		}
	}
	if options.LeaseDuration <= options.OperationTimeout+durableCompletionTimeout {
		return nil, errors.New("watchlist lease must exceed operation and durable-completion timeouts")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Worker{queue: queue, manager: manager, index: index, options: options, now: now}, nil
}

// ProcessOne performs one durable Watchlist submission or reconciliation.
func (w *Worker) ProcessOne(ctx context.Context) (acquisition.WatchlistQueueState, error) {
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
	if claim.BackgroundJobID() == "" {
		return w.submit(ctx, claim)
	}
	return w.reconcile(ctx, claim)
}

func (w *Worker) submit(ctx context.Context, claim acquisition.WatchlistClaim) (acquisition.WatchlistQueueState, error) {
	request, err := claim.SearchRequest()
	if err != nil {
		return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
	}
	operationContext, cancel := context.WithTimeout(ctx, w.options.OperationTimeout)
	defer cancel()
	objectID, found, err := w.index.FindPublished(operationContext, request)
	if err != nil {
		return "", workerBoundaryError(operationContext, "read published media index", err)
	}
	if found {
		completion, completionErr := acquisition.NewWatchlistSucceeded(objectID)
		if completionErr != nil {
			return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
		}
		return w.completeDurably(ctx, claim, completion)
	}
	job, _, submitErr := w.manager.Submit(operationContext, request)
	if submitErr != nil {
		return "", workerBoundaryError(operationContext, "submit watchlist background job", submitErr)
	}
	if err := w.commitDurably(ctx, func(commitContext context.Context) error {
		transitionNow := w.now().UTC()
		return w.queue.AttachJob(
			commitContext, claim, job.ID(), transitionNow.Add(w.options.ReconcileInterval), transitionNow,
		)
	}); err != nil {
		return "", err
	}
	return acquisition.WatchlistQueueStateAcquiring, nil
}

func (w *Worker) reconcile(ctx context.Context, claim acquisition.WatchlistClaim) (acquisition.WatchlistQueueState, error) {
	operationContext, cancel := context.WithTimeout(ctx, w.options.OperationTimeout)
	job, err := w.manager.Get(operationContext, claim.BackgroundJobID())
	cancel()
	if errors.Is(err, domain.ErrNotFound) {
		return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
	}
	if err != nil {
		return "", workerBoundaryError(ctx, "read watchlist background job", err)
	}
	transitionNow := w.now().UTC()
	switch job.State() {
	case acquisition.JobStateQueued, acquisition.JobStateSelected, acquisition.JobStatePreparing:
		if err := w.commitDurably(ctx, func(commitContext context.Context) error {
			return w.queue.DeferJob(
				commitContext, claim, transitionNow.Add(w.options.ReconcileInterval), transitionNow,
			)
		}); err != nil {
			return "", err
		}
		return acquisition.WatchlistQueueStateAcquiring, nil
	case acquisition.JobStateSucceeded:
		completion, completionErr := acquisition.NewWatchlistSucceeded(job.PublishedObjectID())
		if completionErr != nil {
			return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
		}
		return w.completeDurably(ctx, claim, completion)
	case acquisition.JobStateManualReview:
		return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
	case acquisition.JobStateFailed:
		return w.completeFailedJob(ctx, claim, job.ErrorCode(), transitionNow)
	default:
		return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
	}
}

func (w *Worker) completeFailedJob(
	ctx context.Context,
	claim acquisition.WatchlistClaim,
	code acquisition.JobErrorCode,
	now time.Time,
) (acquisition.WatchlistQueueState, error) {
	switch code {
	case acquisition.JobErrorNoRelease, acquisition.JobErrorStalled:
		completion, err := acquisition.NewWatchlistDeferred(
			acquisition.WatchlistQueueStateNotCached, now.Add(w.options.NotCachedCooldown),
		)
		if err != nil {
			return "", err
		}
		return w.completeDurably(ctx, claim, completion)
	case acquisition.JobErrorUnauthorized, acquisition.JobErrorProviderUnavailable:
		completion, err := acquisition.NewWatchlistDeferred(
			acquisition.WatchlistQueueStateRetryable, now.Add(w.options.RetryCooldown),
		)
		if err != nil {
			return "", err
		}
		return w.completeDurably(ctx, claim, completion)
	default:
		return w.completeDurably(ctx, claim, acquisition.NewWatchlistManualReview())
	}
}

func (w *Worker) completeDurably(
	ctx context.Context,
	claim acquisition.WatchlistClaim,
	completion acquisition.WatchlistCompletion,
) (acquisition.WatchlistQueueState, error) {
	if err := w.commitDurably(ctx, func(commitContext context.Context) error {
		return w.queue.Complete(commitContext, claim, completion, w.now().UTC())
	}); err != nil {
		return "", err
	}
	return completion.State(), nil
}

func (w *Worker) commitDurably(ctx context.Context, transition func(context.Context) error) error {
	completionContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), durableCompletionTimeout)
	defer cancel()
	if err := transition(completionContext); err != nil {
		return workerBoundaryError(completionContext, "commit watchlist transition", err)
	}
	return nil
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
	if errors.Is(err, acquisition.ErrStaleWatchlistClaim) {
		return fmt.Errorf("%s: %w", operation, acquisition.ErrStaleWatchlistClaim)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
