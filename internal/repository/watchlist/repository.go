// Package watchlist persists the durable Plex-watchlist ingestion queue.
package watchlist

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

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Repository owns the durable, lease-based watchlist work queue.
type Repository struct {
	db *sql.DB
}

// Open opens the queue database, applies migrations, and seeds the durable
// acquisition policy only when the database has no stored choice yet.
func Open(ctx context.Context, path string, initialAcquisitionEnabled bool) (*Repository, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("watchlist database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create watchlist database directory: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open watchlist database: %w", err)
	}
	database.SetMaxOpenConns(1)
	repository := &Repository{db: database}
	if err := repository.configure(ctx); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	if err := repository.migrate(ctx); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	if err := repository.initializeAcquisitionPolicy(ctx, initialAcquisitionEnabled); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return repository, nil
}

func (r *Repository) initializeAcquisitionPolicy(ctx context.Context, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO watchlist_settings (singleton, acquisition_enabled)
		VALUES (1, ?)
	`, value); err != nil {
		return fmt.Errorf("initialize watchlist acquisition policy: %w", err)
	}
	return nil
}

// AcquisitionEnabled returns the durable automatic-acquisition policy.
func (r *Repository) AcquisitionEnabled(ctx context.Context) (bool, error) {
	policy, err := r.Policy(ctx)
	return policy.AcquisitionEnabled(), err
}

// SetAcquisitionEnabled replaces the durable automatic-acquisition policy.
func (r *Repository) SetAcquisitionEnabled(ctx context.Context, enabled bool) error {
	policy, err := r.Policy(ctx)
	if err != nil {
		return err
	}
	updated, err := acquisitiondomain.NewWatchlistPolicy(enabled, policy.ShowPolicy())
	if err != nil {
		return fmt.Errorf("validate watchlist acquisition policy: %w", err)
	}
	return r.SetPolicy(ctx, updated)
}

// Policy returns the complete durable automatic-acquisition policy.
func (r *Repository) Policy(ctx context.Context) (acquisitiondomain.WatchlistPolicy, error) {
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.WatchlistPolicy{}, fmt.Errorf("read watchlist acquisition policy: %w", err)
	}
	var enabled int
	var showPolicy string
	if err := r.db.QueryRowContext(ctx, `
		SELECT acquisition_enabled, show_policy FROM watchlist_settings WHERE singleton = 1
	`).Scan(&enabled, &showPolicy); err != nil {
		return acquisitiondomain.WatchlistPolicy{}, fmt.Errorf("read watchlist acquisition policy: %w", err)
	}
	policy, err := acquisitiondomain.NewWatchlistPolicy(enabled == 1, acquisitiondomain.WatchlistShowPolicy(showPolicy))
	if err != nil {
		return acquisitiondomain.WatchlistPolicy{}, fmt.Errorf("validate watchlist acquisition policy: %w", err)
	}
	return policy, nil
}

// SetPolicy atomically replaces the complete durable acquisition policy.
func (r *Repository) SetPolicy(ctx context.Context, policy acquisitiondomain.WatchlistPolicy) error {
	validated, err := acquisitiondomain.NewWatchlistPolicy(policy.AcquisitionEnabled(), policy.ShowPolicy())
	if err != nil {
		return fmt.Errorf("validate watchlist acquisition policy: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("set watchlist acquisition policy: %w", err)
	}
	enabled := 0
	if validated.AcquisitionEnabled() {
		enabled = 1
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE watchlist_settings SET acquisition_enabled = ?, show_policy = ? WHERE singleton = 1
	`, enabled, validated.ShowPolicy())
	if err != nil {
		return fmt.Errorf("set watchlist acquisition policy: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read watchlist acquisition policy update: %w", err)
	}
	if rows != 1 {
		return errors.New("watchlist acquisition policy is unavailable")
	}
	return nil
}

func (r *Repository) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure watchlist sqlite: %w", err)
		}
	}
	return nil
}

func (r *Repository) migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS watchlist_schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create watchlist migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read watchlist migrations: %w", err)
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
	err := r.db.QueryRowContext(ctx, "SELECT name FROM watchlist_schema_migrations WHERE name = ?", name).Scan(&applied)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check watchlist migration %s: %w", name, err)
	}
	content, err := migrations.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		return fmt.Errorf("read watchlist migration %s: %w", name, err)
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin watchlist migration %s: %w", name, err)
	}
	if _, err := transaction.ExecContext(ctx, string(content)); err != nil {
		return errors.Join(fmt.Errorf("execute watchlist migration %s: %w", name, err), transaction.Rollback())
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO watchlist_schema_migrations (name) VALUES (?)", name); err != nil {
		return errors.Join(fmt.Errorf("record watchlist migration %s: %w", name, err), transaction.Rollback())
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit watchlist migration %s: %w", name, err)
	}
	return nil
}

// UpsertSnapshot records current provider observations without reopening final
// or deferred work states.
func (r *Repository) UpsertSnapshot(ctx context.Context, items []acquisitiondomain.WatchlistItem, observedAt time.Time) error {
	return r.UpsertSnapshotPolicy(ctx, items, observedAt, true)
}

// UpsertSnapshotPolicy records observations and marks only newly inserted
// movies as eligible for automatic acquisition when explicitly authorized.
func (r *Repository) UpsertSnapshotPolicy(
	ctx context.Context,
	items []acquisitiondomain.WatchlistItem,
	observedAt time.Time,
	autoEligible bool,
) error {
	observations := make([]acquisitiondomain.WatchlistObservation, 0, len(items))
	for _, item := range items {
		eligible := autoEligible && item.MediaType() == acquisitiondomain.WatchlistMediaTypeMovie
		observation, err := acquisitiondomain.NewWatchlistObservation(item, eligible, 0, 0)
		if err != nil {
			return fmt.Errorf("validate watchlist snapshot item: %w", err)
		}
		observations = append(observations, observation)
	}
	return r.UpsertObservations(ctx, observations, observedAt)
}

// UpsertObservations persists validated provider observations and immutable
// acquisition intent without reopening existing queue rows.
func (r *Repository) UpsertObservations(
	ctx context.Context,
	observations []acquisitiondomain.WatchlistObservation,
	observedAt time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upsert watchlist snapshot: %w", err)
	}
	if observedAt.IsZero() {
		return errors.New("watchlist observation time is required")
	}
	validated := make([]acquisitiondomain.WatchlistObservation, 0, len(observations))
	for _, observation := range observations {
		value, err := validateObservation(observation)
		if err != nil {
			return err
		}
		validated = append(validated, value)
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin watchlist snapshot: %w", err)
	}
	timestamp := observedAt.UTC().UnixMilli()
	for _, observation := range validated {
		item := observation.Item()
		eligible := 0
		if observation.AutoEligible() {
			eligible = 1
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO watchlist_queue (
				source, external_id, media_type, title, release_year,
				first_observed_unix_ms, last_observed_unix_ms, updated_unix_ms, auto_eligible,
				intent_season, intent_episode
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source, external_id) DO UPDATE SET
				media_type = excluded.media_type,
				title = excluded.title,
				release_year = excluded.release_year,
				last_observed_unix_ms = excluded.last_observed_unix_ms,
				updated_unix_ms = excluded.updated_unix_ms
		`, item.Source(), item.ExternalID(), item.MediaType(), item.Title(), item.Year(), timestamp, timestamp, timestamp,
			eligible, observation.Season(), observation.Episode()); err != nil {
			return errors.Join(fmt.Errorf("upsert watchlist item: %w", err), transaction.Rollback())
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit watchlist snapshot: %w", err)
	}
	return nil
}

// Claim atomically leases one eligible exact movie or episode intent.
func (r *Repository) Claim(ctx context.Context, now time.Time, leaseDuration time.Duration) (acquisitiondomain.WatchlistClaim, error) {
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.WatchlistClaim{}, fmt.Errorf("claim watchlist item: %w", err)
	}
	if now.IsZero() {
		return acquisitiondomain.WatchlistClaim{}, errors.New("watchlist claim time is required")
	}
	if leaseDuration <= 0 {
		return acquisitiondomain.WatchlistClaim{}, errors.New("watchlist lease duration must be positive")
	}
	nowMillis := now.UTC().UnixMilli()
	leaseUntilMillis := now.Add(leaseDuration).UTC().UnixMilli()
	row := r.db.QueryRowContext(ctx, `
		WITH eligible AS (
			SELECT rowid
			FROM watchlist_queue
			WHERE EXISTS (
				SELECT 1 FROM watchlist_settings
				WHERE singleton = 1 AND acquisition_enabled = 1
			)
			  AND auto_eligible = 1
			  AND (
				media_type = 'movie'
				OR (
					media_type = 'show'
					AND EXISTS (
						SELECT 1 FROM watchlist_settings
						WHERE singleton = 1 AND show_policy = 'pilot'
					)
				)
			  )
			  AND (
				(state IN ('pending', 'not_cached', 'retryable') AND next_attempt_unix_ms <= ?)
				OR (state = 'acquiring' AND lease_until_unix_ms <= ? AND next_attempt_unix_ms <= ?)
			  )
			ORDER BY
				CASE WHEN state = 'acquiring' THEN 0 ELSE 1 END,
				CASE WHEN state = 'acquiring' THEN lease_until_unix_ms ELSE next_attempt_unix_ms END,
				first_observed_unix_ms,
				source,
				external_id
			LIMIT 1
		)
		UPDATE watchlist_queue
		SET state = 'acquiring',
			attempt_count = attempt_count + 1,
			lease_version = lease_version + 1,
			lease_until_unix_ms = ?,
			updated_unix_ms = ?
		WHERE rowid = (SELECT rowid FROM eligible)
		RETURNING source, external_id, media_type, title, release_year,
			intent_season, intent_episode, lease_version, attempt_count, background_job_id
	`, nowMillis, nowMillis, nowMillis, leaseUntilMillis, nowMillis)
	claim, err := scanClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return acquisitiondomain.WatchlistClaim{}, domain.ErrNotFound
	}
	if err != nil {
		return acquisitiondomain.WatchlistClaim{}, fmt.Errorf("claim watchlist item: %w", err)
	}
	return claim, nil
}

// AttachJob durably links a claimed Watchlist movie to a background acquisition.
func (r *Repository) AttachJob(
	ctx context.Context,
	claim acquisitiondomain.WatchlistClaim,
	jobID string,
	nextAttempt time.Time,
) error {
	validated, err := validateClaimWithJob(claim, jobID)
	if err != nil {
		return fmt.Errorf("validate watchlist job attachment: %w", err)
	}
	return r.transitionJob(ctx, validated, nextAttempt, jobID, "attach")
}

// DeferJob releases a linked Watchlist claim until its next reconciliation.
func (r *Repository) DeferJob(ctx context.Context, claim acquisitiondomain.WatchlistClaim, nextAttempt time.Time) error {
	if claim.BackgroundJobID() == "" {
		return errors.New("deferred watchlist job requires a linked background job")
	}
	validated, err := validateClaimWithJob(claim, claim.BackgroundJobID())
	if err != nil {
		return fmt.Errorf("validate deferred watchlist job: %w", err)
	}
	return r.transitionJob(ctx, validated, nextAttempt, validated.BackgroundJobID(), "defer")
}

func (r *Repository) transitionJob(
	ctx context.Context,
	claim acquisitiondomain.WatchlistClaim,
	nextAttempt time.Time,
	jobID string,
	operation string,
) error {
	if nextAttempt.IsZero() {
		return fmt.Errorf("%s watchlist job requires a next attempt time", operation)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s watchlist job: %w", operation, err)
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE watchlist_queue
		SET state = 'acquiring', lease_until_unix_ms = 0, next_attempt_unix_ms = ?,
			background_job_id = ?, updated_unix_ms = ?
		WHERE source = ? AND external_id = ? AND state = 'acquiring' AND lease_version = ?
	`, nextAttempt.UTC().UnixMilli(), jobID, time.Now().UTC().UnixMilli(),
		claim.Item().Source(), claim.Item().ExternalID(), claim.LeaseVersion())
	if err != nil {
		return fmt.Errorf("%s watchlist job: %w", operation, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s watchlist job: %w", operation, err)
	}
	if rows != 1 {
		return acquisitiondomain.ErrStaleWatchlistClaim
	}
	return nil
}

// Complete commits a result only when the caller still owns the exact lease.
func (r *Repository) Complete(ctx context.Context, claim acquisitiondomain.WatchlistClaim, completion acquisitiondomain.WatchlistCompletion) error {
	validatedClaim, err := validateClaim(claim)
	if err != nil {
		return fmt.Errorf("invalid watchlist completion claim: %w", err)
	}
	validatedCompletion, err := validateCompletion(completion)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("complete watchlist item: %w", err)
	}
	nextAttemptMillis := int64(0)
	if !validatedCompletion.NextAttempt().IsZero() {
		nextAttemptMillis = validatedCompletion.NextAttempt().UTC().UnixMilli()
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE watchlist_queue
		SET state = ?, lease_until_unix_ms = 0, next_attempt_unix_ms = ?,
			published_object_id = ?, background_job_id = '', updated_unix_ms = ?
		WHERE source = ? AND external_id = ? AND state = 'acquiring' AND lease_version = ?
	`, validatedCompletion.State(), nextAttemptMillis, validatedCompletion.PublishedObjectID(),
		time.Now().UTC().UnixMilli(), validatedClaim.Item().Source(), validatedClaim.Item().ExternalID(), validatedClaim.LeaseVersion())
	if err != nil {
		return fmt.Errorf("complete watchlist item: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect watchlist completion: %w", err)
	}
	if rows != 1 {
		return acquisitiondomain.ErrStaleWatchlistClaim
	}
	return nil
}

// CanAdvanceEpisode reports whether one exact published show episode remains
// eligible for automatic progression under the current durable policy.
func (r *Repository) CanAdvanceEpisode(
	ctx context.Context,
	source string,
	externalID string,
	publishedObjectID string,
	current domain.EpisodeCoordinate,
	observedAfter time.Time,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("check watchlist episode advancement: %w", err)
	}
	identity, err := validateEpisodeFrontier(source, externalID, publishedObjectID, current, observedAfter)
	if err != nil {
		return false, fmt.Errorf("validate watchlist episode advancement: %w", err)
	}
	var eligible int
	err = r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM watchlist_queue
			WHERE source = ?
			  AND external_id = ?
			  AND media_type = 'show'
			  AND state = 'succeeded'
			  AND auto_eligible = 1
			  AND published_object_id = ?
			  AND intent_season = ?
			  AND intent_episode = ?
			  AND last_observed_unix_ms >= ?
			  AND EXISTS (
				SELECT 1
				FROM watchlist_settings
				WHERE singleton = 1
				  AND acquisition_enabled = 1
				  AND show_policy = 'pilot'
			  )
		)
	`, identity.source, identity.externalID, identity.publishedObjectID,
		identity.coordinate.Season(), identity.coordinate.Episode(), identity.observedAfterUnixMillis).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("check watchlist episode advancement: %w", err)
	}
	return eligible == 1, nil
}

// AdvanceEpisode atomically moves one exact succeeded episode frontier to the
// next pending intent while preserving the already-published media manifest.
func (r *Repository) AdvanceEpisode(
	ctx context.Context,
	source string,
	externalID string,
	publishedObjectID string,
	current domain.EpisodeCoordinate,
	next domain.EpisodeCoordinate,
	observedAfter time.Time,
	now time.Time,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("advance watchlist episode: %w", err)
	}
	identity, err := validateEpisodeFrontier(source, externalID, publishedObjectID, current, observedAfter)
	if err != nil {
		return false, fmt.Errorf("validate watchlist episode advancement: %w", err)
	}
	validatedNext, err := domain.NewEpisodeCoordinate(next.Season(), next.Episode())
	if err != nil {
		return false, fmt.Errorf("validate next watchlist episode: %w", err)
	}
	if !validatedNext.After(identity.coordinate) {
		return false, errors.New("next watchlist episode must follow the current episode")
	}
	if now.IsZero() {
		return false, errors.New("watchlist episode advancement time is required")
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE watchlist_queue
		SET state = 'pending',
			attempt_count = 0,
			lease_until_unix_ms = 0,
			next_attempt_unix_ms = 0,
			published_object_id = '',
			background_job_id = '',
			intent_season = ?,
			intent_episode = ?,
			updated_unix_ms = ?
		WHERE source = ?
		  AND external_id = ?
		  AND media_type = 'show'
		  AND state = 'succeeded'
		  AND auto_eligible = 1
		  AND published_object_id = ?
		  AND intent_season = ?
		  AND intent_episode = ?
		  AND last_observed_unix_ms >= ?
		  AND EXISTS (
			SELECT 1
			FROM watchlist_settings
			WHERE singleton = 1
			  AND acquisition_enabled = 1
			  AND show_policy = 'pilot'
		  )
	`, validatedNext.Season(), validatedNext.Episode(), now.UTC().UnixMilli(),
		identity.source, identity.externalID, identity.publishedObjectID,
		identity.coordinate.Season(), identity.coordinate.Episode(), identity.observedAfterUnixMillis)
	if err != nil {
		return false, fmt.Errorf("advance watchlist episode: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect watchlist episode advancement: %w", err)
	}
	return rows == 1, nil
}

// Status returns aggregate counts without exposing private watchlist titles.
func (r *Repository) Status(ctx context.Context) (acquisitiondomain.WatchlistQueueStatus, error) {
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.WatchlistQueueStatus{}, fmt.Errorf("read watchlist status: %w", err)
	}
	var status acquisitiondomain.WatchlistQueueStatus
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN media_type = 'movie' AND state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'acquiring' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'succeeded' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'not_cached' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'retryable' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'manual_review' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN media_type = 'show' THEN 1 ELSE 0 END), 0)
		FROM watchlist_queue
	`).Scan(&status.PendingMovies, &status.Acquiring, &status.Succeeded, &status.NotCached,
		&status.Retryable, &status.ManualReview, &status.ObservedShows)
	if err != nil {
		return acquisitiondomain.WatchlistQueueStatus{}, fmt.Errorf("read watchlist status: %w", err)
	}
	return status, nil
}

// Close closes the queue database.
func (r *Repository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close watchlist database: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanClaim(scanner rowScanner) (acquisitiondomain.WatchlistClaim, error) {
	var input acquisitiondomain.WatchlistItemInput
	var mediaType string
	var leaseVersion int64
	var attempt int
	var backgroundJobID string
	var season int
	var episode int
	if err := scanner.Scan(
		&input.Source, &input.ExternalID, &mediaType, &input.Title, &input.Year,
		&season, &episode, &leaseVersion, &attempt, &backgroundJobID,
	); err != nil {
		return acquisitiondomain.WatchlistClaim{}, err
	}
	input.MediaType = acquisitiondomain.WatchlistMediaType(mediaType)
	item, err := acquisitiondomain.NewWatchlistItem(input)
	if err != nil {
		return acquisitiondomain.WatchlistClaim{}, fmt.Errorf("validate persisted watchlist item: %w", err)
	}
	observation, err := acquisitiondomain.NewWatchlistObservation(item, true, season, episode)
	if err != nil {
		return acquisitiondomain.WatchlistClaim{}, fmt.Errorf("validate persisted watchlist intent: %w", err)
	}
	if backgroundJobID != "" {
		return acquisitiondomain.NewWatchlistIntentJobClaim(observation, leaseVersion, attempt, backgroundJobID)
	}
	return acquisitiondomain.NewWatchlistIntentClaim(observation, leaseVersion, attempt)
}

func validateObservation(observation acquisitiondomain.WatchlistObservation) (acquisitiondomain.WatchlistObservation, error) {
	validated, err := acquisitiondomain.NewWatchlistObservation(
		observation.Item(), observation.AutoEligible(), observation.Season(), observation.Episode(),
	)
	if err != nil {
		return acquisitiondomain.WatchlistObservation{}, fmt.Errorf("invalid watchlist observation: %w", err)
	}
	return validated, nil
}

func validateClaim(claim acquisitiondomain.WatchlistClaim) (acquisitiondomain.WatchlistClaim, error) {
	observation, err := acquisitiondomain.NewWatchlistObservation(
		claim.Item(), claim.AutoEligible(), claim.Season(), claim.Episode(),
	)
	if err != nil {
		return acquisitiondomain.WatchlistClaim{}, err
	}
	return acquisitiondomain.NewWatchlistIntentClaim(observation, claim.LeaseVersion(), claim.Attempt())
}

func validateClaimWithJob(claim acquisitiondomain.WatchlistClaim, jobID string) (acquisitiondomain.WatchlistClaim, error) {
	observation, err := acquisitiondomain.NewWatchlistObservation(
		claim.Item(), claim.AutoEligible(), claim.Season(), claim.Episode(),
	)
	if err != nil {
		return acquisitiondomain.WatchlistClaim{}, err
	}
	return acquisitiondomain.NewWatchlistIntentJobClaim(
		observation, claim.LeaseVersion(), claim.Attempt(), jobID,
	)
}

func validateCompletion(completion acquisitiondomain.WatchlistCompletion) (acquisitiondomain.WatchlistCompletion, error) {
	switch completion.State() {
	case acquisitiondomain.WatchlistQueueStateSucceeded:
		return acquisitiondomain.NewWatchlistSucceeded(completion.PublishedObjectID())
	case acquisitiondomain.WatchlistQueueStateNotCached, acquisitiondomain.WatchlistQueueStateRetryable:
		return acquisitiondomain.NewWatchlistDeferred(completion.State(), completion.NextAttempt())
	case acquisitiondomain.WatchlistQueueStateManualReview:
		return acquisitiondomain.NewWatchlistManualReview(), nil
	default:
		return acquisitiondomain.WatchlistCompletion{}, errors.New("invalid watchlist completion state")
	}
}

type episodeFrontierIdentity struct {
	source                  string
	externalID              string
	publishedObjectID       string
	coordinate              domain.EpisodeCoordinate
	observedAfterUnixMillis int64
}

func validateEpisodeFrontier(
	source string,
	externalID string,
	publishedObjectID string,
	coordinate domain.EpisodeCoordinate,
	observedAfter time.Time,
) (episodeFrontierIdentity, error) {
	item, err := acquisitiondomain.NewWatchlistItem(acquisitiondomain.WatchlistItemInput{
		Source: source, ExternalID: externalID, MediaType: acquisitiondomain.WatchlistMediaTypeShow,
		Title: "Episode frontier", Year: 2026,
	})
	if err != nil {
		return episodeFrontierIdentity{}, err
	}
	completion, err := acquisitiondomain.NewWatchlistSucceeded(publishedObjectID)
	if err != nil {
		return episodeFrontierIdentity{}, err
	}
	validatedCoordinate, err := domain.NewEpisodeCoordinate(coordinate.Season(), coordinate.Episode())
	if err != nil {
		return episodeFrontierIdentity{}, err
	}
	if observedAfter.IsZero() {
		return episodeFrontierIdentity{}, errors.New("watchlist observation cutoff is required")
	}
	return episodeFrontierIdentity{
		source: item.Source(), externalID: item.ExternalID(), publishedObjectID: completion.PublishedObjectID(),
		coordinate: validatedCoordinate, observedAfterUnixMillis: observedAfter.UTC().UnixMilli(),
	}, nil
}
