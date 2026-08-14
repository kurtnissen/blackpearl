// Package watchlist coordinates privacy-safe Plex watchlist observation.
package watchlist

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

// ErrUnavailable is the public-safe observation boundary failure.
var ErrUnavailable = errors.New("watchlist observation is temporarily unavailable")

// SnapshotGateway reads one current provider watchlist snapshot.
type SnapshotGateway interface {
	Snapshot(ctx context.Context) ([]acquisitiondomain.WatchlistItem, error)
}

// QueueRepository persists observations and returns privacy-safe counts.
type QueueRepository interface {
	UpsertObservations(ctx context.Context, observations []acquisitiondomain.WatchlistObservation, observedAt time.Time) error
	Status(ctx context.Context) (acquisitiondomain.WatchlistQueueStatus, error)
	Policy(ctx context.Context) (acquisitiondomain.WatchlistPolicy, error)
	SetPolicy(ctx context.Context, policy acquisitiondomain.WatchlistPolicy) error
}

// ObserverOptions configures serialized observation polling.
type ObserverOptions struct {
	PollInterval time.Duration
	Now          func() time.Time
}

// ObserverStatus is safe to return only through the paired local API. It never
// contains watchlist titles, identifiers, or credentials.
type ObserverStatus struct {
	Enabled            bool                                   `json:"enabled"`
	Healthy            bool                                   `json:"healthy"`
	AcquisitionEnabled bool                                   `json:"acquisitionEnabled"`
	ShowPolicy         acquisitiondomain.WatchlistShowPolicy  `json:"showPolicy"`
	LastSyncAt         *time.Time                             `json:"lastSyncAt,omitempty"`
	Queue              acquisitiondomain.WatchlistQueueStatus `json:"queue"`
}

// Observer polls and durably records watchlist metadata without acquiring it.
type Observer struct {
	gateway      SnapshotGateway
	queue        QueueRepository
	pollInterval time.Duration
	now          func() time.Time

	syncMu           sync.Mutex
	mu               sync.RWMutex
	healthy          bool
	lastSyncAt       *time.Time
	baselineComplete bool
}

// NewObserver constructs an observe-only watchlist coordinator.
func NewObserver(gateway SnapshotGateway, queue QueueRepository, options ObserverOptions) (*Observer, error) {
	if gateway == nil || queue == nil {
		return nil, errors.New("watchlist observer dependencies are required")
	}
	if options.PollInterval <= 0 {
		return nil, errors.New("watchlist poll interval must be positive")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Observer{
		gateway: gateway, queue: queue, pollInterval: options.PollInterval,
		now: now,
	}, nil
}

// Sync reads and persists one snapshot without calling acquisition providers.
func (o *Observer) Sync(ctx context.Context) error {
	o.syncMu.Lock()
	defer o.syncMu.Unlock()
	ctx, span := otel.Tracer("blackpearl/watchlist").Start(ctx, "watchlist.sync")
	defer span.End()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync Plex watchlist: %w", err)
	}
	items, err := o.gateway.Snapshot(ctx)
	if err != nil {
		o.markUnhealthy()
		return publicError(ctx, "read Plex watchlist", err)
	}
	observedAt := o.now().UTC()
	if observedAt.IsZero() {
		o.markUnhealthy()
		return fmt.Errorf("record Plex watchlist: %w", ErrUnavailable)
	}
	policy, err := o.queue.Policy(ctx)
	if err != nil {
		o.markUnhealthy()
		return publicError(ctx, "read watchlist acquisition policy", err)
	}
	o.mu.RLock()
	baselineComplete := o.baselineComplete
	o.mu.RUnlock()
	observations := make([]acquisitiondomain.WatchlistObservation, 0, len(items))
	for _, item := range items {
		autoEligible := baselineComplete && policy.AcquisitionEnabled()
		season, episode := 0, 0
		if item.MediaType() == acquisitiondomain.WatchlistMediaTypeShow {
			autoEligible = autoEligible && policy.ShowPolicy() == acquisitiondomain.WatchlistShowPolicyPilot
			if autoEligible {
				season, episode = 1, 1
			}
		}
		observation, observationErr := acquisitiondomain.NewWatchlistObservation(item, autoEligible, season, episode)
		if observationErr != nil {
			o.markUnhealthy()
			return fmt.Errorf("validate Plex watchlist observation: %w", ErrUnavailable)
		}
		observations = append(observations, observation)
	}
	if err := o.queue.UpsertObservations(ctx, observations, observedAt); err != nil {
		o.markUnhealthy()
		return publicError(ctx, "record Plex watchlist", err)
	}
	o.mu.Lock()
	o.healthy = true
	o.lastSyncAt = &observedAt
	o.baselineComplete = true
	o.mu.Unlock()
	return nil
}

// Run observes immediately and then at a fixed interval until cancellation.
// Individual provider failures are retained in status and retried later.
func (o *Observer) Run(ctx context.Context) {
	if err := o.Sync(ctx); err != nil && ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := o.Sync(ctx); err != nil && ctx.Err() != nil {
				return
			}
		}
	}
}

// Status returns current aggregate queue and observation health.
func (o *Observer) Status(ctx context.Context) (ObserverStatus, error) {
	if err := ctx.Err(); err != nil {
		return ObserverStatus{}, fmt.Errorf("read watchlist status: %w", err)
	}
	queueStatus, err := o.queue.Status(ctx)
	if err != nil {
		return ObserverStatus{}, publicError(ctx, "read watchlist status", err)
	}
	policy, err := o.queue.Policy(ctx)
	if err != nil {
		return ObserverStatus{}, publicError(ctx, "read watchlist acquisition policy", err)
	}
	o.mu.RLock()
	status := ObserverStatus{
		Enabled: true, Healthy: o.healthy, AcquisitionEnabled: policy.AcquisitionEnabled(),
		ShowPolicy: policy.ShowPolicy(), Queue: queueStatus,
	}
	if o.lastSyncAt != nil {
		lastSyncAt := *o.lastSyncAt
		status.LastSyncAt = &lastSyncAt
	}
	o.mu.RUnlock()
	return status, nil
}

// SetPolicy changes the durable automatic-acquisition policy.
func (o *Observer) SetPolicy(ctx context.Context, policy acquisitiondomain.WatchlistPolicy) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("set watchlist acquisition policy: %w", err)
	}
	if err := o.queue.SetPolicy(ctx, policy); err != nil {
		return publicError(ctx, "set watchlist acquisition policy", err)
	}
	return nil
}

// SetAcquisitionEnabled changes only the master acquisition switch while
// preserving the current explicit show policy.
func (o *Observer) SetAcquisitionEnabled(ctx context.Context, enabled bool) error {
	policy, err := o.queue.Policy(ctx)
	if err != nil {
		return publicError(ctx, "read watchlist acquisition policy", err)
	}
	updated, err := acquisitiondomain.NewWatchlistPolicy(enabled, policy.ShowPolicy())
	if err != nil {
		return fmt.Errorf("set watchlist acquisition policy: %w", ErrUnavailable)
	}
	return o.SetPolicy(ctx, updated)
}

func (o *Observer) markUnhealthy() {
	o.mu.Lock()
	o.healthy = false
	o.mu.Unlock()
}

func publicError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	if errors.Is(err, domain.ErrUnauthorized) {
		return fmt.Errorf("%s: %w", operation, domain.ErrUnauthorized)
	}
	return fmt.Errorf("%s: %w", operation, ErrUnavailable)
}
