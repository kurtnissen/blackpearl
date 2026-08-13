// Package core contains BlackPearl's catalog business logic.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"go.opentelemetry.io/otel"
)

const (
	pocID        domain.MediaID = "blackpearl-poc-2026"
	pocTitle     string         = "BlackPearl POC"
	pocYear      int            = 2026
	pocExtension string         = ".mp4"
)

// Repository is the catalog persistence required by Catalog.
type Repository interface {
	Upsert(ctx context.Context, media domain.Media) error
	GetByVirtualPath(ctx context.Context, virtualPath string) (domain.Media, error)
	List(ctx context.Context) ([]domain.Media, error)
	Ping(ctx context.Context) error
}

// POCImporter imports the legal Milestone 1 fixture without exposing a path to consumers.
type POCImporter interface {
	Import(ctx context.Context, source string) (backing domain.BackingRef, size int64, err error)
}

// MediaSource opens logical, range-readable media without promising complete local bytes.
type MediaSource interface {
	Open(ctx context.Context, media domain.Media) (domain.ReadHandle, error)
	Ready(ctx context.Context) error
}

// Catalog orchestrates media metadata and cached bytes.
type Catalog struct {
	repository Repository
	importer   POCImporter
	source     MediaSource
}

// NewCatalog constructs a catalog service from its narrow boundaries.
func NewCatalog(repository Repository, importer POCImporter, source MediaSource) *Catalog {
	return &Catalog{repository: repository, importer: importer, source: source}
}

// ImportPOC imports the legal synthetic fixture and persists its canonical catalog entry.
func (c *Catalog) ImportPOC(ctx context.Context, source string) (domain.Media, error) {
	ctx, span := otel.Tracer("blackpearl/core").Start(ctx, "catalog.import_poc")
	defer span.End()
	backing, size, err := c.importer.Import(ctx, source)
	if err != nil {
		return domain.Media{}, fmt.Errorf("import POC fixture: %w", err)
	}
	media, err := domain.NewMovie(pocID, pocTitle, pocYear, pocExtension, size, backing)
	if err != nil {
		return domain.Media{}, fmt.Errorf("construct POC media: %w", err)
	}
	if err := c.repository.Upsert(ctx, media); err != nil {
		return domain.Media{}, fmt.Errorf("persist POC media: %w", err)
	}
	return media, nil
}

// List returns the catalog in stable virtual-path order.
func (c *Catalog) List(ctx context.Context) ([]domain.Media, error) {
	media, err := c.repository.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list catalog: %w", err)
	}
	return media, nil
}

// Lookup resolves a PearlFS path to catalog metadata.
func (c *Catalog) Lookup(ctx context.Context, virtualPath string) (domain.Media, error) {
	media, err := c.repository.GetByVirtualPath(ctx, virtualPath)
	if err != nil {
		return domain.Media{}, fmt.Errorf("lookup catalog path: %w", err)
	}
	return media, nil
}

// Open resolves a PearlFS path and opens its immutable cached bytes.
func (c *Catalog) Open(ctx context.Context, virtualPath string) (domain.ReadHandle, error) {
	media, err := c.Lookup(ctx, virtualPath)
	if err != nil {
		return nil, err
	}
	reader, err := c.source.Open(ctx, media)
	if err != nil {
		return nil, fmt.Errorf("open cached media: %w", err)
	}
	if reader.Size() != media.Size {
		closeErr := reader.Close()
		return nil, errors.Join(
			fmt.Errorf("cached media size mismatch: catalog=%d cache=%d", media.Size, reader.Size()),
			closeErr,
		)
	}
	return reader, nil
}

// Ready verifies catalog persistence and the selected media source without opening a complete object.
func (c *Catalog) Ready(ctx context.Context) error {
	if err := c.repository.Ping(ctx); err != nil {
		return fmt.Errorf("catalog repository is not ready: %w", err)
	}
	items, err := c.repository.List(ctx)
	if err != nil {
		return fmt.Errorf("list catalog for readiness: %w", err)
	}
	if len(items) == 0 {
		return errors.New("catalog is empty")
	}
	if err := c.source.Ready(ctx); err != nil {
		return fmt.Errorf("media source is not ready: %w", err)
	}
	return nil
}
