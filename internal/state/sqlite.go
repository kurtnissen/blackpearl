// Package state implements BlackPearl's SQLite catalog repository.
package state

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

	"github.com/blackpearl-media/blackpearl/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Repository persists the BlackPearl catalog in SQLite.
type Repository struct {
	db *sql.DB
}

// Open opens a SQLite catalog and applies embedded migrations.
func Open(ctx context.Context, path string) (*Repository, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database path must be absolute: %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	repository := &Repository{db: database}
	if err := repository.configure(ctx); err != nil {
		closeErr := database.Close()
		return nil, errors.Join(err, closeErr)
	}
	if err := repository.migrate(ctx); err != nil {
		closeErr := database.Close()
		return nil, errors.Join(err, closeErr)
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
			return fmt.Errorf("configure sqlite with %q: %w", statement, err)
		}
	}
	return nil
}

func (r *Repository) migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
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
	err := r.db.QueryRowContext(ctx, "SELECT name FROM schema_migrations WHERE name = ?", name).Scan(&applied)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check migration %s: %w", name, err)
	}
	content, err := migrations.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	if _, err := transaction.ExecContext(ctx, string(content)); err != nil {
		rollbackErr := transaction.Rollback()
		return errors.Join(fmt.Errorf("execute migration %s: %w", name, err), rollbackErr)
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO schema_migrations (name) VALUES (?)", name); err != nil {
		rollbackErr := transaction.Rollback()
		return errors.Join(fmt.Errorf("record migration %s: %w", name, err), rollbackErr)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// Upsert creates or updates a catalog item.
func (r *Repository) Upsert(ctx context.Context, media domain.Media) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO media (
			id, media_type, title, release_year, extension, virtual_path, size_bytes,
			backing_provider, backing_object_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			media_type = excluded.media_type,
			title = excluded.title,
			release_year = excluded.release_year,
			extension = excluded.extension,
			virtual_path = excluded.virtual_path,
			size_bytes = excluded.size_bytes,
			backing_provider = excluded.backing_provider,
			backing_object_id = excluded.backing_object_id,
			updated_at = CURRENT_TIMESTAMP
	`,
		media.ID,
		media.Type,
		media.Title,
		media.Year,
		media.Extension,
		media.VirtualPath,
		media.Size,
		media.Backing.Provider,
		media.Backing.ObjectID,
	)
	if err != nil {
		return fmt.Errorf("upsert media %s: %w", media.ID, err)
	}
	return nil
}

// GetByVirtualPath finds one catalog item by its PearlFS path.
func (r *Repository) GetByVirtualPath(ctx context.Context, virtualPath string) (domain.Media, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, media_type, title, release_year, extension, virtual_path, size_bytes,
		       backing_provider, backing_object_id
		FROM media
		WHERE virtual_path = ?
	`, virtualPath)
	media, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Media{}, fmt.Errorf("%w: media path %q", domain.ErrNotFound, virtualPath)
	}
	if err != nil {
		return domain.Media{}, fmt.Errorf("get media by virtual path: %w", err)
	}
	return media, nil
}

// List returns all catalog items in stable virtual-path order.
func (r *Repository) List(ctx context.Context) ([]domain.Media, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, media_type, title, release_year, extension, virtual_path, size_bytes,
		       backing_provider, backing_object_id
		FROM media
		ORDER BY virtual_path
	`)
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	var result []domain.Media
	for rows.Next() {
		media, scanErr := scanMedia(rows)
		if scanErr != nil {
			closeErr := rows.Close()
			return nil, errors.Join(fmt.Errorf("scan listed media: %w", scanErr), closeErr)
		}
		result = append(result, media)
	}
	if err := rows.Err(); err != nil {
		closeErr := rows.Close()
		return nil, errors.Join(fmt.Errorf("iterate listed media: %w", err), closeErr)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close listed media rows: %w", err)
	}
	return result, nil
}

// Ping verifies that SQLite is available.
func (r *Repository) Ping(ctx context.Context) error {
	if err := r.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

// Close closes the SQLite database.
func (r *Repository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMedia(scanner rowScanner) (domain.Media, error) {
	var media domain.Media
	if err := scanner.Scan(
		&media.ID,
		&media.Type,
		&media.Title,
		&media.Year,
		&media.Extension,
		&media.VirtualPath,
		&media.Size,
		&media.Backing.Provider,
		&media.Backing.ObjectID,
	); err != nil {
		return domain.Media{}, err
	}
	return media, nil
}
