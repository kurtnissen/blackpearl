package acquisitionjob

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
	"go.opentelemetry.io/otel"
)

const durableTransitionTimeout = 5 * time.Second

// WorkerQueue is the lease and transition boundary consumed by the worker.
type WorkerQueue interface {
	Claim(ctx context.Context, now time.Time, leaseDuration time.Duration) (acquisition.AcquisitionJobClaim, error)
	Plan(ctx context.Context, claim acquisition.AcquisitionJobClaim, candidates []acquisition.JobCandidate, now time.Time) error
	AttachPrepared(ctx context.Context, claim acquisition.AcquisitionJobClaim, created acquisition.CreatedObject, createdByJob bool, now time.Time) error
	Advance(ctx context.Context, claim acquisition.AcquisitionJobClaim, outcome acquisition.CandidateOutcome, terminalCode acquisition.JobErrorCode, now time.Time) (bool, error)
	Defer(ctx context.Context, claim acquisition.AcquisitionJobClaim, nextAttempt time.Time, code acquisition.JobErrorCode, progress int, now time.Time) error
	Succeed(ctx context.Context, claim acquisition.AcquisitionJobClaim, publishedObjectID string, now time.Time) error
	Fail(ctx context.Context, claim acquisition.AcquisitionJobClaim, code acquisition.JobErrorCode, manualReview bool, now time.Time) error
}

// Searcher returns policy-ranked provider releases.
type Searcher interface {
	Search(ctx context.Context, request acquisition.SearchRequest) ([]acquisition.Release, error)
}

// Materializer resolves an ephemeral release into transient provider input.
type Materializer interface {
	Materialize(ctx context.Context, release acquisition.Release) (acquisition.TorrentInput, error)
}

// Preparer reconciles, creates, and inspects provider account objects.
type Preparer interface {
	CachedTorrents(ctx context.Context, releases []acquisition.Release) ([]acquisition.Release, error)
	FindTorrentByHash(ctx context.Context, infoHash string) (acquisition.CreatedObject, error)
	CreateTorrent(ctx context.Context, input acquisition.TorrentInput, allowDownload bool) (acquisition.CreatedObject, error)
	InspectCreatedTorrent(ctx context.Context, created acquisition.CreatedObject) ([]domain.MediaCandidate, error)
}

// Publisher atomically exposes one completed media item to Plex's filesystem.
type Publisher interface {
	PublishAcquired(ctx context.Context, media acquisition.AcquiredMedia) error
}

// Providers is one request-local set created from private saved credentials.
type Providers struct {
	Searcher     Searcher
	Materializer Materializer
	Preparer     Preparer
}

// ProviderFactory builds fresh provider gateways without exposing credentials
// to the worker or durable job model.
type ProviderFactory func(ctx context.Context) (Providers, error)

// WorkerOptions bounds leases, calls, polling, and retries.
type WorkerOptions struct {
	LeaseDuration         time.Duration
	OperationTimeout      time.Duration
	IdleInterval          time.Duration
	PreparingPollInterval time.Duration
	RetryInterval         time.Duration
	Now                   func() time.Time
	OnError               func(error)
}

// Worker serially advances at most one durable transition per claimed lease.
type Worker struct {
	queue     WorkerQueue
	providers ProviderFactory
	publisher Publisher
	options   WorkerOptions
	now       func() time.Time
	processMu sync.Mutex
}

// NewWorker constructs a serialized durable acquisition worker.
func NewWorker(queue WorkerQueue, providers ProviderFactory, publisher Publisher, options WorkerOptions) (*Worker, error) {
	if queue == nil || providers == nil || publisher == nil {
		return nil, errors.New("background acquisition worker dependencies are required")
	}
	for name, value := range map[string]time.Duration{
		"lease duration": options.LeaseDuration, "operation timeout": options.OperationTimeout,
		"idle interval": options.IdleInterval, "preparing poll interval": options.PreparingPollInterval,
		"retry interval": options.RetryInterval,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("background acquisition worker %s must be positive", name)
		}
	}
	if options.LeaseDuration <= options.OperationTimeout+durableTransitionTimeout {
		return nil, errors.New("background acquisition lease must exceed operation and durable-transition timeouts")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Worker{queue: queue, providers: providers, publisher: publisher, options: options, now: now}, nil
}

// ProcessOne advances at most one durable job transition.
func (w *Worker) ProcessOne(ctx context.Context) (acquisition.JobState, error) {
	w.processMu.Lock()
	defer w.processMu.Unlock()
	ctx, span := otel.Tracer("blackpearl/acquisitionjob").Start(ctx, "acquisition_job.process_one")
	defer span.End()
	claim, err := w.queue.Claim(ctx, w.now().UTC(), w.options.LeaseDuration)
	if err != nil {
		return "", fmt.Errorf("claim background acquisition: %w", err)
	}
	operationContext, cancel := context.WithTimeout(ctx, w.options.OperationTimeout)
	defer cancel()
	providers, err := w.providers(operationContext)
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	if providers.Searcher == nil || providers.Materializer == nil || providers.Preparer == nil {
		return w.deferProviderFailure(ctx, claim, errors.New("provider set is incomplete"))
	}
	switch claim.Job().State() {
	case acquisition.JobStateQueued:
		return w.resolve(ctx, operationContext, claim, providers)
	case acquisition.JobStateSelected:
		return w.prepare(ctx, operationContext, claim, providers)
	case acquisition.JobStatePreparing:
		return w.publish(ctx, operationContext, claim, providers)
	default:
		return "", errors.New("claimed background acquisition has unsupported state")
	}
}

func (w *Worker) resolve(ctx context.Context, operationContext context.Context, claim acquisition.AcquisitionJobClaim, providers Providers) (acquisition.JobState, error) {
	releases, err := providers.Searcher.Search(operationContext, claim.Job().Request())
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	eligible := make([]acquisition.Release, 0, acquisition.MaximumJobCandidates)
	seen := make(map[string]struct{}, acquisition.MaximumJobCandidates)
	for _, release := range releases {
		if release.Protocol() != acquisition.ReleaseProtocolTorrent || release.InfoHash() == "" {
			continue
		}
		hash := release.InfoHash()
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		eligible = append(eligible, release)
		if len(eligible) == acquisition.MaximumJobCandidates {
			break
		}
	}
	if len(eligible) == 0 {
		return w.fail(ctx, claim, acquisition.JobErrorNoRelease, false)
	}
	cached, err := providers.Preparer.CachedTorrents(operationContext, eligible)
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	ordered := cachedFirst(eligible, cached)
	candidates := make([]acquisition.JobCandidate, 0, len(ordered))
	for ordinal, release := range ordered {
		selection, selectionErr := acquisition.NewJobSelection(release)
		if selectionErr != nil {
			continue
		}
		outcome := acquisition.CandidateOutcomePending
		if len(candidates) == 0 {
			outcome = acquisition.CandidateOutcomeSelected
		}
		candidate, candidateErr := acquisition.NewJobCandidate(selection, ordinal, outcome)
		if candidateErr != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return w.fail(ctx, claim, acquisition.JobErrorNoRelease, false)
	}
	if err := w.commit(ctx, func(commitContext context.Context) error {
		return w.queue.Plan(commitContext, claim, candidates, w.now().UTC())
	}); err != nil {
		return "", err
	}
	return acquisition.JobStateSelected, nil
}

func cachedFirst(eligible []acquisition.Release, cached []acquisition.Release) []acquisition.Release {
	cachedHashes := make(map[string]struct{}, len(cached))
	eligibleHashes := make(map[string]struct{}, len(eligible))
	for _, release := range eligible {
		eligibleHashes[release.InfoHash()] = struct{}{}
	}
	for _, release := range cached {
		if _, eligible := eligibleHashes[release.InfoHash()]; eligible {
			cachedHashes[release.InfoHash()] = struct{}{}
		}
	}
	ordered := make([]acquisition.Release, 0, len(eligible))
	for _, release := range eligible {
		if _, isCached := cachedHashes[release.InfoHash()]; isCached {
			ordered = append(ordered, release)
		}
	}
	for _, release := range eligible {
		if _, isCached := cachedHashes[release.InfoHash()]; !isCached {
			ordered = append(ordered, release)
		}
	}
	return ordered
}

func (w *Worker) prepare(ctx context.Context, operationContext context.Context, claim acquisition.AcquisitionJobClaim, providers Providers) (acquisition.JobState, error) {
	selection := claim.Job().Selection()
	created, err := providers.Preparer.FindTorrentByHash(operationContext, selection.InfoHash())
	switch {
	case err == nil:
		return w.attach(ctx, claim, created)
	case errors.Is(err, acquisition.ErrAmbiguousProviderObjects):
		return w.fail(ctx, claim, acquisition.JobErrorAmbiguousMutation, true)
	case errors.Is(err, domain.ErrUnauthorized):
		return w.deferProviderFailure(ctx, claim, err)
	case !errors.Is(err, domain.ErrNotFound):
		return w.deferProviderFailure(ctx, claim, err)
	}
	releases, err := providers.Searcher.Search(operationContext, claim.Job().Request())
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	var ephemeral acquisition.Release
	for _, release := range releases {
		if release.Protocol() == acquisition.ReleaseProtocolTorrent && release.InfoHash() == selection.InfoHash() {
			ephemeral = release
			break
		}
	}
	if ephemeral.InfoHash() == "" {
		return w.fail(ctx, claim, acquisition.JobErrorMaterialization, false)
	}
	material, err := providers.Materializer.Materialize(operationContext, ephemeral)
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	created, createErr := providers.Preparer.CreateTorrent(operationContext, material, true)
	if createErr == nil {
		return w.attach(ctx, claim, created)
	}
	if errors.Is(createErr, domain.ErrUnauthorized) {
		return w.deferProviderFailure(ctx, claim, createErr)
	}
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.options.OperationTimeout)
	defer cancel()
	reconciled, reconcileErr := providers.Preparer.FindTorrentByHash(reconcileContext, selection.InfoHash())
	if reconcileErr == nil {
		return w.attach(ctx, claim, reconciled)
	}
	return w.fail(ctx, claim, acquisition.JobErrorAmbiguousMutation, true)
}

func (w *Worker) publish(ctx context.Context, operationContext context.Context, claim acquisition.AcquisitionJobClaim, providers Providers) (acquisition.JobState, error) {
	candidates, err := providers.Preparer.InspectCreatedTorrent(operationContext, claim.Job().CreatedObject())
	if errors.Is(err, acquisition.ErrStalled) {
		return w.fail(ctx, claim, acquisition.JobErrorStalled, false)
	}
	if errors.Is(err, acquisition.ErrNotReady) {
		return w.deferJob(ctx, claim, acquisition.JobErrorNone, w.options.PreparingPollInterval)
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, acquisition.ErrAmbiguousProviderObjects) {
		return w.fail(ctx, claim, acquisition.JobErrorAmbiguousMutation, true)
	}
	if err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	selected, err := acquisitionservice.SelectCandidate(claim.Job().Request(), candidates)
	if err != nil {
		return w.fail(ctx, claim, acquisition.JobErrorNoPlayableMedia, false)
	}
	media, err := acquisition.NewAcquiredMedia(claim.Job().Request(), claim.Job().Selection().Release(), selected)
	if err != nil {
		return w.fail(ctx, claim, acquisition.JobErrorNoPlayableMedia, false)
	}
	if err := w.publisher.PublishAcquired(operationContext, media); err != nil {
		return w.deferProviderFailure(ctx, claim, err)
	}
	if err := w.commit(ctx, func(commitContext context.Context) error {
		return w.queue.Succeed(commitContext, claim, media.Candidate().ObjectID, w.now().UTC())
	}); err != nil {
		return "", err
	}
	return acquisition.JobStateSucceeded, nil
}

func (w *Worker) attach(ctx context.Context, claim acquisition.AcquisitionJobClaim, created acquisition.CreatedObject) (acquisition.JobState, error) {
	if err := w.commit(ctx, func(commitContext context.Context) error {
		return w.queue.AttachPrepared(commitContext, claim, created, false, w.now().UTC())
	}); err != nil {
		return "", err
	}
	return acquisition.JobStatePreparing, nil
}

func (w *Worker) fail(ctx context.Context, claim acquisition.AcquisitionJobClaim, code acquisition.JobErrorCode, manualReview bool) (acquisition.JobState, error) {
	if err := w.commit(ctx, func(commitContext context.Context) error {
		return w.queue.Fail(commitContext, claim, code, manualReview, w.now().UTC())
	}); err != nil {
		return "", err
	}
	if manualReview {
		return acquisition.JobStateManualReview, nil
	}
	return acquisition.JobStateFailed, nil
}

func (w *Worker) deferProviderFailure(ctx context.Context, claim acquisition.AcquisitionJobClaim, cause error) (acquisition.JobState, error) {
	if (errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) && ctx.Err() != nil {
		return "", ctx.Err()
	}
	code := acquisition.JobErrorProviderUnavailable
	if errors.Is(cause, domain.ErrUnauthorized) {
		code = acquisition.JobErrorUnauthorized
	}
	return w.deferJob(ctx, claim, code, w.options.RetryInterval)
}

func (w *Worker) deferJob(ctx context.Context, claim acquisition.AcquisitionJobClaim, code acquisition.JobErrorCode, delay time.Duration) (acquisition.JobState, error) {
	now := w.now().UTC()
	if err := w.commit(ctx, func(commitContext context.Context) error {
		return w.queue.Defer(commitContext, claim, now.Add(delay), code, claim.Job().Progress(), now)
	}); err != nil {
		return "", err
	}
	return claim.Job().State(), nil
}

func (w *Worker) commit(ctx context.Context, transition func(context.Context) error) error {
	commitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), durableTransitionTimeout)
	defer cancel()
	if err := transition(commitContext); err != nil {
		return fmt.Errorf("commit background acquisition transition: %w", err)
	}
	return nil
}

// Run advances jobs until the process lifecycle context is canceled.
func (w *Worker) Run(ctx context.Context) {
	for {
		_, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, context.Canceled) && w.options.OnError != nil {
			w.options.OnError(err)
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
