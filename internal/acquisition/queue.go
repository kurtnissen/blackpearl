package acquisition

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const maximumPublishedObjectIDBytes = 512

// ErrStaleWatchlistClaim means a worker tried to finish a lease that has
// already expired and been claimed by another worker.
var ErrStaleWatchlistClaim = errors.New("stale watchlist claim")

// WatchlistQueueState is the durable lifecycle of one observed watchlist item.
type WatchlistQueueState string

const (
	// WatchlistQueueStatePending is eligible for its first acquisition attempt.
	WatchlistQueueStatePending WatchlistQueueState = "pending"
	// WatchlistQueueStateAcquiring has an active, time-bounded worker lease.
	WatchlistQueueStateAcquiring WatchlistQueueState = "acquiring"
	// WatchlistQueueStateSucceeded has been published and is final.
	WatchlistQueueStateSucceeded WatchlistQueueState = "succeeded"
	// WatchlistQueueStateNotCached waits for a future cached-only retry.
	WatchlistQueueStateNotCached WatchlistQueueState = "not_cached"
	// WatchlistQueueStateRetryable waits after a transient provider failure.
	WatchlistQueueStateRetryable WatchlistQueueState = "retryable"
	// WatchlistQueueStateManualReview prevents an ambiguous mutation retry.
	WatchlistQueueStateManualReview WatchlistQueueState = "manual_review"
)

// WatchlistClaim is one immutable, versioned queue lease.
type WatchlistClaim struct {
	observation     WatchlistObservation
	leaseVersion    int64
	attempt         int
	backgroundJobID string
}

// NewWatchlistClaim validates a queue lease loaded from persistence.
func NewWatchlistClaim(item WatchlistItem, leaseVersion int64, attempt int) (WatchlistClaim, error) {
	observation, err := NewWatchlistObservation(item, true, 0, 0)
	if err != nil {
		return WatchlistClaim{}, err
	}
	return NewWatchlistIntentClaim(observation, leaseVersion, attempt)
}

// NewWatchlistJobClaim validates a queue lease linked to a durable acquisition job.
func NewWatchlistJobClaim(item WatchlistItem, leaseVersion int64, attempt int, jobID string) (WatchlistClaim, error) {
	observation, err := NewWatchlistObservation(item, true, 0, 0)
	if err != nil {
		return WatchlistClaim{}, err
	}
	return NewWatchlistIntentJobClaim(observation, leaseVersion, attempt, jobID)
}

// NewWatchlistIntentClaim validates an exact queue lease loaded from persistence.
func NewWatchlistIntentClaim(observation WatchlistObservation, leaseVersion int64, attempt int) (WatchlistClaim, error) {
	return newWatchlistClaim(observation, leaseVersion, attempt, "")
}

// NewWatchlistIntentJobClaim validates an exact queue lease linked to a durable job.
func NewWatchlistIntentJobClaim(
	observation WatchlistObservation,
	leaseVersion int64,
	attempt int,
	jobID string,
) (WatchlistClaim, error) {
	if !jobIDPattern.MatchString(jobID) {
		return WatchlistClaim{}, errors.New("watchlist background job ID must be 32 lowercase hexadecimal characters")
	}
	return newWatchlistClaim(observation, leaseVersion, attempt, jobID)
}

func newWatchlistClaim(observation WatchlistObservation, leaseVersion int64, attempt int, jobID string) (WatchlistClaim, error) {
	validated, err := NewWatchlistObservation(
		observation.Item(), observation.AutoEligible(), observation.Season(), observation.Episode(),
	)
	if err != nil {
		return WatchlistClaim{}, fmt.Errorf("invalid watchlist claim intent: %w", err)
	}
	if !validated.AutoEligible() {
		return WatchlistClaim{}, errors.New("watchlist claim requires eligible acquisition intent")
	}
	if leaseVersion < 1 {
		return WatchlistClaim{}, errors.New("watchlist claim lease version must be positive")
	}
	if attempt < 1 {
		return WatchlistClaim{}, errors.New("watchlist claim attempt must be positive")
	}
	return WatchlistClaim{observation: validated, leaseVersion: leaseVersion, attempt: attempt, backgroundJobID: jobID}, nil
}

// Item returns the validated watchlist intent owned by this lease.
func (c WatchlistClaim) Item() WatchlistItem { return c.observation.Item() }

// AutoEligible reports whether the first observation authorized this intent.
func (c WatchlistClaim) AutoEligible() bool { return c.observation.AutoEligible() }

// Season returns the exact episode season, or zero for movies.
func (c WatchlistClaim) Season() int { return c.observation.Season() }

// Episode returns the exact episode number, or zero for movies.
func (c WatchlistClaim) Episode() int { return c.observation.Episode() }

// SearchRequest returns the exact acquisition intent persisted with this claim.
func (c WatchlistClaim) SearchRequest() (SearchRequest, error) {
	item := c.Item()
	if item.MediaType() == WatchlistMediaTypeMovie {
		return item.SearchRequest()
	}
	if item.MediaType() == WatchlistMediaTypeShow {
		return NewEpisodeSearch(item.Title(), item.Year(), c.Season(), c.Episode())
	}
	return SearchRequest{}, ErrUnsupportedWatchlistMedia
}

// LeaseVersion returns the optimistic concurrency token for this lease.
func (c WatchlistClaim) LeaseVersion() int64 { return c.leaseVersion }

// Attempt returns the number of times this item has been claimed.
func (c WatchlistClaim) Attempt() int { return c.attempt }

// BackgroundJobID returns the durable acquisition job linked to this lease.
func (c WatchlistClaim) BackgroundJobID() string { return c.backgroundJobID }

// WatchlistCompletion is a validated terminal or deferred lease result.
type WatchlistCompletion struct {
	state             WatchlistQueueState
	nextAttempt       time.Time
	publishedObjectID string
}

// NewWatchlistSucceeded creates a final result for published media.
func NewWatchlistSucceeded(publishedObjectID string) (WatchlistCompletion, error) {
	for _, character := range publishedObjectID {
		if unicode.IsControl(character) {
			return WatchlistCompletion{}, errors.New("published object ID must not contain control characters")
		}
	}
	objectID := strings.TrimSpace(publishedObjectID)
	if objectID == "" {
		return WatchlistCompletion{}, errors.New("published object ID is required")
	}
	if len(objectID) > maximumPublishedObjectIDBytes {
		return WatchlistCompletion{}, fmt.Errorf("published object ID must not exceed %d bytes", maximumPublishedObjectIDBytes)
	}
	return WatchlistCompletion{state: WatchlistQueueStateSucceeded, publishedObjectID: objectID}, nil
}

// NewWatchlistDeferred creates a bounded future retry result.
func NewWatchlistDeferred(state WatchlistQueueState, nextAttempt time.Time) (WatchlistCompletion, error) {
	if state != WatchlistQueueStateNotCached && state != WatchlistQueueStateRetryable {
		return WatchlistCompletion{}, errors.New("deferred watchlist state must be not_cached or retryable")
	}
	if nextAttempt.IsZero() {
		return WatchlistCompletion{}, errors.New("deferred watchlist completion requires a next attempt time")
	}
	return WatchlistCompletion{state: state, nextAttempt: nextAttempt.UTC()}, nil
}

// NewWatchlistManualReview creates a final result for an ambiguous mutation.
func NewWatchlistManualReview() WatchlistCompletion {
	return WatchlistCompletion{state: WatchlistQueueStateManualReview}
}

// State returns the validated durable outcome state.
func (c WatchlistCompletion) State() WatchlistQueueState { return c.state }

// NextAttempt returns the future retry time, or zero for final outcomes.
func (c WatchlistCompletion) NextAttempt() time.Time { return c.nextAttempt }

// PublishedObjectID returns the published backing object for a success.
func (c WatchlistCompletion) PublishedObjectID() string { return c.publishedObjectID }

// WatchlistQueueStatus contains privacy-safe aggregate queue counts.
type WatchlistQueueStatus struct {
	PendingMovies int `json:"pendingMovies"`
	Acquiring     int `json:"acquiring"`
	Succeeded     int `json:"succeeded"`
	NotCached     int `json:"notCached"`
	Retryable     int `json:"retryable"`
	ManualReview  int `json:"manualReview"`
	ObservedShows int `json:"observedShows"`
}
