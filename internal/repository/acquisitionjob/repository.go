// Package acquisitionjob persists the durable, lease-based background
// acquisition queue.
package acquisitionjob

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	_ "modernc.org/sqlite"
)

// ErrStaleClaim means a worker no longer owns the lease used for a transition.
var ErrStaleClaim = errors.New("stale acquisition job claim")

//go:embed migrations/*.sql
var migrations embed.FS

// Repository owns durable background acquisition jobs.
type Repository struct {
	db *sql.DB
}

// Open opens the SQLite database and applies embedded migrations.
func Open(ctx context.Context, path string) (*Repository, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("acquisition job database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create acquisition job database directory: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open acquisition job database: %w", err)
	}
	database.SetMaxOpenConns(1)
	repository := &Repository{db: database}
	if err := repository.configure(ctx); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	if err := repository.migrate(ctx); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return repository, nil
}

func (r *Repository) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure acquisition job sqlite: %w", err)
		}
	}
	return nil
}

func (r *Repository) migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS acquisition_job_schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create acquisition job migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read acquisition job migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if err := r.applyMigration(ctx, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) applyMigration(ctx context.Context, name string) error {
	var applied string
	err := r.db.QueryRowContext(ctx, "SELECT name FROM acquisition_job_schema_migrations WHERE name = ?", name).Scan(&applied)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check acquisition job migration %s: %w", name, err)
	}
	content, err := migrations.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		return fmt.Errorf("read acquisition job migration %s: %w", name, err)
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin acquisition job migration %s: %w", name, err)
	}
	if _, err := transaction.ExecContext(ctx, string(content)); err != nil {
		return errors.Join(fmt.Errorf("execute acquisition job migration %s: %w", name, err), transaction.Rollback())
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO acquisition_job_schema_migrations (name) VALUES (?)", name); err != nil {
		return errors.Join(fmt.Errorf("record acquisition job migration %s: %w", name, err), transaction.Rollback())
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit acquisition job migration %s: %w", name, err)
	}
	return nil
}

// Submit inserts a queued job or returns the existing active job for the same
// normalized media intent.
func (r *Repository) Submit(ctx context.Context, id string, request acquisition.SearchRequest, now time.Time) (acquisition.AcquisitionJob, bool, error) {
	if now.IsZero() {
		return acquisition.AcquisitionJob{}, false, errors.New("acquisition job submission time is required")
	}
	queued, err := acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: id, Request: request, State: acquisition.JobStateQueued,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	})
	if err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("validate acquisition job submission: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("submit acquisition job: %w", err)
	}
	intentKey := jobIntentKey(queued.Request())
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("begin acquisition job submission: %w", err)
	}
	existing, err := queryJob(transaction.QueryRowContext(ctx, selectJobSQL+` WHERE intent_key = ? AND state IN ('queued', 'selected', 'preparing') LIMIT 1`, intentKey))
	if err == nil {
		return existing, false, transaction.Rollback()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return acquisition.AcquisitionJob{}, false, errors.Join(fmt.Errorf("find active acquisition job: %w", err), transaction.Rollback())
	}
	timestamp := now.UTC().UnixMilli()
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO acquisition_jobs (
			id, intent_key, media_type, title, release_year, season, episode, state,
			created_unix_ms, updated_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)
	`, queued.ID(), intentKey, queued.Request().MediaType(), queued.Request().Title(), queued.Request().Year(),
		queued.Request().Season(), queued.Request().Episode(), timestamp, timestamp)
	if err != nil {
		return acquisition.AcquisitionJob{}, false, errors.Join(fmt.Errorf("insert acquisition job: %w", err), transaction.Rollback())
	}
	if err := transaction.Commit(); err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("commit acquisition job submission: %w", err)
	}
	return queued, true, nil
}

// Claim leases one eligible nonterminal job.
func (r *Repository) Claim(ctx context.Context, now time.Time, leaseDuration time.Duration) (acquisition.AcquisitionJobClaim, error) {
	if now.IsZero() {
		return acquisition.AcquisitionJobClaim{}, errors.New("acquisition job claim time is required")
	}
	if leaseDuration <= 0 {
		return acquisition.AcquisitionJobClaim{}, errors.New("acquisition job lease duration must be positive")
	}
	nowMillis := now.UTC().UnixMilli()
	row := r.db.QueryRowContext(ctx, `
		WITH eligible AS (
			SELECT id FROM acquisition_jobs
			WHERE state IN ('queued', 'selected', 'preparing')
			  AND next_attempt_unix_ms <= ?
			  AND lease_until_unix_ms <= ?
			ORDER BY next_attempt_unix_ms, created_unix_ms, id
			LIMIT 1
		)
		UPDATE acquisition_jobs
		SET attempt_count = attempt_count + 1,
			lease_version = lease_version + 1,
			lease_until_unix_ms = ?,
			updated_unix_ms = ?
		WHERE id = (SELECT id FROM eligible)
		RETURNING `+jobColumns+`
	`, nowMillis, nowMillis, now.Add(leaseDuration).UTC().UnixMilli(), nowMillis)
	job, err := queryJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return acquisition.AcquisitionJobClaim{}, domain.ErrNotFound
	}
	if err != nil {
		return acquisition.AcquisitionJobClaim{}, fmt.Errorf("claim acquisition job: %w", err)
	}
	var leaseVersion int64
	if err := r.db.QueryRowContext(ctx, "SELECT lease_version FROM acquisition_jobs WHERE id = ?", job.ID()).Scan(&leaseVersion); err != nil {
		return acquisition.AcquisitionJobClaim{}, fmt.Errorf("read acquisition job lease version: %w", err)
	}
	claim, err := acquisition.NewAcquisitionJobClaim(job, leaseVersion)
	if err != nil {
		return acquisition.AcquisitionJobClaim{}, fmt.Errorf("validate acquisition job claim: %w", err)
	}
	return claim, nil
}

// Select durably records the stable release fingerprint before provider mutation.
func (r *Repository) Select(ctx context.Context, claim acquisition.AcquisitionJobClaim, selection acquisition.JobSelection, now time.Time) error {
	validated, err := acquisition.NewJobSelection(selection.Release())
	if err != nil {
		return fmt.Errorf("validate acquisition job selection: %w", err)
	}
	seeders, hasSeeders := 0, 0
	if validated.HasSeeders() {
		seeders, hasSeeders = validated.Seeders(), 1
	}
	return r.transition(ctx, claim, acquisition.JobStateQueued, now, `
		state = 'selected', selected_provider = ?, selected_title = ?, selected_size = ?,
		selected_indexer = ?, selected_info_hash = ?, selected_seeders = ?, selected_has_seeders = ?,
		error_code = '', progress = 0, next_attempt_unix_ms = 0
	`, validated.Provider(), validated.Title(), validated.Size(), validated.Indexer(), validated.InfoHash(), seeders, hasSeeders)
}

// Attach records the stable provider account object after reconciliation or creation.
func (r *Repository) Attach(ctx context.Context, claim acquisition.AcquisitionJobClaim, created acquisition.CreatedObject, now time.Time) error {
	validated, err := acquisition.NewCreatedObject(created.Provider(), created.ObjectID())
	if err != nil {
		return fmt.Errorf("validate acquisition job created object: %w", err)
	}
	return r.transition(ctx, claim, acquisition.JobStateSelected, now, `
		state = 'preparing', created_provider = ?, created_object_id = ?, error_code = '', progress = 0,
		next_attempt_unix_ms = 0
	`, validated.Provider(), validated.ObjectID())
}

// Defer releases a lease while preserving its durable stage for a bounded retry.
func (r *Repository) Defer(
	ctx context.Context,
	claim acquisition.AcquisitionJobClaim,
	nextAttempt time.Time,
	code acquisition.JobErrorCode,
	progress int,
	now time.Time,
) error {
	if nextAttempt.IsZero() || !nextAttempt.After(now) {
		return errors.New("deferred acquisition job requires a future attempt time")
	}
	if progress < 0 || progress >= 100 {
		return errors.New("deferred acquisition job progress must be between 0 and 99")
	}
	if code != acquisition.JobErrorProviderUnavailable && code != acquisition.JobErrorUnauthorized {
		return errors.New("deferred acquisition job requires a retryable public error code")
	}
	return r.transitionAnyActive(ctx, claim, now, `
		next_attempt_unix_ms = ?, error_code = ?, progress = ?
	`, nextAttempt.UTC().UnixMilli(), code, progress)
}

// Succeed records a final published range-readable media object.
func (r *Repository) Succeed(ctx context.Context, claim acquisition.AcquisitionJobClaim, publishedObjectID string, now time.Time) error {
	input := acquisition.JobSnapshotInput{
		ID: claim.Job().ID(), Request: claim.Job().Request(), State: acquisition.JobStateSucceeded,
		Selection: pointerSelection(claim.Job()), CreatedObject: pointerCreatedObject(claim.Job()),
		PublishedObjectID: publishedObjectID, Attempt: claim.Job().Attempt(), Progress: 100,
		CreatedAt: claim.Job().CreatedAt(), UpdatedAt: now,
	}
	validated, err := acquisition.NewAcquisitionJobSnapshot(input)
	if err != nil {
		return fmt.Errorf("validate successful acquisition job: %w", err)
	}
	return r.transition(ctx, claim, acquisition.JobStatePreparing, now, `
		state = 'succeeded', published_object_id = ?, error_code = '', progress = 100,
		next_attempt_unix_ms = 0
	`, validated.PublishedObjectID())
}

// Fail records a terminal public-safe outcome. manualReview is reserved for an
// ambiguous provider mutation that must never be retried automatically.
func (r *Repository) Fail(ctx context.Context, claim acquisition.AcquisitionJobClaim, code acquisition.JobErrorCode, manualReview bool, now time.Time) error {
	state := acquisition.JobStateFailed
	if manualReview {
		state = acquisition.JobStateManualReview
	}
	input := acquisition.JobSnapshotInput{
		ID: claim.Job().ID(), Request: claim.Job().Request(), State: state,
		Selection: pointerSelection(claim.Job()), CreatedObject: pointerCreatedObject(claim.Job()),
		ErrorCode: code, Attempt: claim.Job().Attempt(), Progress: claim.Job().Progress(),
		CreatedAt: claim.Job().CreatedAt(), UpdatedAt: now,
	}
	if _, err := acquisition.NewAcquisitionJobSnapshot(input); err != nil {
		return fmt.Errorf("validate failed acquisition job: %w", err)
	}
	return r.transitionAnyActive(ctx, claim, now, "state = ?, error_code = ?, next_attempt_unix_ms = 0", state, code)
}

func pointerSelection(job acquisition.AcquisitionJob) *acquisition.JobSelection {
	if !job.HasSelection() {
		return nil
	}
	value := job.Selection()
	return &value
}

func pointerCreatedObject(job acquisition.AcquisitionJob) *acquisition.CreatedObject {
	if !job.HasCreatedObject() {
		return nil
	}
	value := job.CreatedObject()
	return &value
}

func (r *Repository) transition(ctx context.Context, claim acquisition.AcquisitionJobClaim, from acquisition.JobState, now time.Time, assignments string, args ...any) error {
	return r.transitionWithStates(ctx, claim, []acquisition.JobState{from}, now, assignments, args...)
}

func (r *Repository) transitionAnyActive(ctx context.Context, claim acquisition.AcquisitionJobClaim, now time.Time, assignments string, args ...any) error {
	return r.transitionWithStates(ctx, claim, []acquisition.JobState{
		acquisition.JobStateQueued, acquisition.JobStateSelected, acquisition.JobStatePreparing,
	}, now, assignments, args...)
}

func (r *Repository) transitionWithStates(
	ctx context.Context,
	claim acquisition.AcquisitionJobClaim,
	states []acquisition.JobState,
	now time.Time,
	assignments string,
	args ...any,
) error {
	if now.IsZero() {
		return errors.New("acquisition job transition time is required")
	}
	if claim.Job().ID() == "" || claim.LeaseVersion() < 1 {
		return errors.New("acquisition job transition requires a validated claim")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(states)), ",")
	query := "UPDATE acquisition_jobs SET " + assignments + `,
		lease_until_unix_ms = 0, updated_unix_ms = ?
		WHERE id = ? AND lease_version = ? AND lease_until_unix_ms > ? AND state IN (` + placeholders + ")"
	arguments := append([]any{}, args...)
	nowMillis := now.UTC().UnixMilli()
	arguments = append(arguments, nowMillis, claim.Job().ID(), claim.LeaseVersion(), nowMillis)
	for _, state := range states {
		arguments = append(arguments, state)
	}
	result, err := r.db.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("transition acquisition job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect acquisition job transition: %w", err)
	}
	if rows != 1 {
		return ErrStaleClaim
	}
	return nil
}

// Get returns one job by its public identifier.
func (r *Repository) Get(ctx context.Context, id string) (acquisition.AcquisitionJob, error) {
	if !validJobID(id) {
		return acquisition.AcquisitionJob{}, errors.New("invalid acquisition job ID")
	}
	job, err := queryJob(r.db.QueryRowContext(ctx, selectJobSQL+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return acquisition.AcquisitionJob{}, domain.ErrNotFound
	}
	if err != nil {
		return acquisition.AcquisitionJob{}, fmt.Errorf("get acquisition job: %w", err)
	}
	return job, nil
}

// List returns the most recently updated jobs without private provider data.
func (r *Repository) List(ctx context.Context, limit int) (result []acquisition.AcquisitionJob, resultErr error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("acquisition job list limit must be between 1 and 100")
	}
	rows, err := r.db.QueryContext(ctx, selectJobSQL+" ORDER BY updated_unix_ms DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("list acquisition jobs: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result = make([]acquisition.AcquisitionJob, 0)
	for rows.Next() {
		job, scanErr := queryJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan acquisition job list: %w", scanErr)
		}
		result = append(result, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate acquisition job list: %w", err)
	}
	return result, nil
}

// Close closes the queue database.
func (r *Repository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close acquisition job database: %w", err)
	}
	return nil
}

const jobColumns = `
	id, media_type, title, release_year, season, episode, state,
	selected_provider, selected_title, selected_size, selected_indexer,
	selected_info_hash, selected_seeders, selected_has_seeders,
	created_provider, created_object_id, published_object_id, error_code,
	attempt_count, progress, created_unix_ms, updated_unix_ms`

const selectJobSQL = `SELECT ` + jobColumns + ` FROM acquisition_jobs`

type rowScanner interface {
	Scan(dest ...any) error
}

func queryJob(row rowScanner) (acquisition.AcquisitionJob, error) {
	var id, mediaType, title, state string
	var selectedProvider, selectedTitle, selectedIndexer, selectedHash string
	var createdProvider, createdObjectID, publishedObjectID, errorCode string
	var year, season, episode, selectedSeeders, selectedHasSeeders, attempt, progress int
	var selectedSize, createdMillis, updatedMillis int64
	if err := row.Scan(
		&id, &mediaType, &title, &year, &season, &episode, &state,
		&selectedProvider, &selectedTitle, &selectedSize, &selectedIndexer,
		&selectedHash, &selectedSeeders, &selectedHasSeeders,
		&createdProvider, &createdObjectID, &publishedObjectID, &errorCode,
		&attempt, &progress, &createdMillis, &updatedMillis,
	); err != nil {
		return acquisition.AcquisitionJob{}, err
	}
	request, err := persistedRequest(domain.MediaType(mediaType), title, year, season, episode)
	if err != nil {
		return acquisition.AcquisitionJob{}, fmt.Errorf("validate persisted acquisition intent: %w", err)
	}
	var selection *acquisition.JobSelection
	if selectedHash != "" {
		var seeders *int
		if selectedHasSeeders == 1 {
			seeders = &selectedSeeders
		}
		release, releaseErr := acquisition.NewRelease(acquisition.ReleaseInput{
			Provider: selectedProvider, SourceID: "torrent:" + selectedHash,
			Title: selectedTitle, Protocol: acquisition.ReleaseProtocolTorrent,
			Size: selectedSize, Indexer: selectedIndexer, InfoHash: selectedHash, Seeders: seeders,
		})
		if releaseErr != nil {
			return acquisition.AcquisitionJob{}, fmt.Errorf("validate persisted acquisition release: %w", releaseErr)
		}
		value, selectionErr := acquisition.NewJobSelection(release)
		if selectionErr != nil {
			return acquisition.AcquisitionJob{}, fmt.Errorf("validate persisted acquisition selection: %w", selectionErr)
		}
		selection = &value
	}
	var created *acquisition.CreatedObject
	if createdObjectID != "" {
		value, createdErr := acquisition.NewCreatedObject(createdProvider, createdObjectID)
		if createdErr != nil {
			return acquisition.AcquisitionJob{}, fmt.Errorf("validate persisted acquisition object: %w", createdErr)
		}
		created = &value
	}
	return acquisition.NewAcquisitionJobSnapshot(acquisition.JobSnapshotInput{
		ID: id, Request: request, State: acquisition.JobState(state),
		Selection: selection, CreatedObject: created, PublishedObjectID: publishedObjectID,
		ErrorCode: acquisition.JobErrorCode(errorCode), Attempt: attempt, Progress: progress,
		CreatedAt: time.UnixMilli(createdMillis).UTC(), UpdatedAt: time.UnixMilli(updatedMillis).UTC(),
	})
}

func persistedRequest(mediaType domain.MediaType, title string, year int, season int, episode int) (acquisition.SearchRequest, error) {
	switch mediaType {
	case domain.MediaTypeMovie:
		return acquisition.NewMovieSearch(title, year)
	case domain.MediaTypeEpisode:
		return acquisition.NewEpisodeSearch(title, year, season, episode)
	default:
		return acquisition.SearchRequest{}, fmt.Errorf("unsupported persisted media type: %q", mediaType)
	}
}

func jobIntentKey(request acquisition.SearchRequest) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d", request.MediaType(), strings.ToLower(request.Title()), request.Year(), request.Season(), request.Episode())
}

func validJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
