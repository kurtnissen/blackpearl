// Package core contains BlackPearl's catalog business logic.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

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

// RegisterRemoteMovie persists one provider-backed logical movie without importing bytes.
func (c *Catalog) RegisterRemoteMovie(ctx context.Context, configuration domain.SetupConfiguration, backing domain.BackingRef) (domain.Media, error) {
	ctx, span := otel.Tracer("blackpearl/core").Start(ctx, "catalog.register_remote_movie")
	defer span.End()
	validated, err := domain.NewSetupConfiguration(configuration.Candidate(), configuration.Title, configuration.Year)
	if err != nil {
		return domain.Media{}, fmt.Errorf("validate selected movie: %w", err)
	}
	validatedBacking, err := domain.NewBackingRef(backing.Provider, backing.ObjectID)
	if err != nil {
		return domain.Media{}, fmt.Errorf("validate selected movie backing: %w", err)
	}
	media, err := domain.NewMovie(remoteMediaID(validatedBacking), validated.Title, validated.Year, validated.Extension, validated.Size, validatedBacking)
	if err != nil {
		return domain.Media{}, fmt.Errorf("construct selected movie: %w", err)
	}
	if err := c.repository.Upsert(ctx, media); err != nil {
		return domain.Media{}, fmt.Errorf("persist selected movie: %w", err)
	}
	return media, nil
}

// RegisterRemoteEpisode persists one provider-backed logical TV episode without importing bytes.
func (c *Catalog) RegisterRemoteEpisode(ctx context.Context, configuration domain.SetupConfiguration, backing domain.BackingRef) (domain.Media, error) {
	ctx, span := otel.Tracer("blackpearl/core").Start(ctx, "catalog.register_remote_episode")
	defer span.End()
	validated, err := domain.NewSetupEpisodeConfiguration(
		configuration.Candidate(), configuration.ShowTitle, configuration.Year,
		configuration.Season, configuration.Episode, configuration.Title,
	)
	if err != nil {
		return domain.Media{}, fmt.Errorf("validate selected episode: %w", err)
	}
	validatedBacking, err := domain.NewBackingRef(backing.Provider, backing.ObjectID)
	if err != nil {
		return domain.Media{}, fmt.Errorf("validate selected episode backing: %w", err)
	}
	media, err := domain.NewEpisode(
		remoteMediaID(validatedBacking), validated.ShowTitle, validated.Year,
		validated.Season, validated.Episode, validated.Title, validated.Extension,
		validated.Size, validatedBacking,
	)
	if err != nil {
		return domain.Media{}, fmt.Errorf("construct selected episode: %w", err)
	}
	if err := c.repository.Upsert(ctx, media); err != nil {
		return domain.Media{}, fmt.Errorf("persist selected episode: %w", err)
	}
	return media, nil
}

func remoteMediaID(backing domain.BackingRef) domain.MediaID {
	digest := sha256.Sum256([]byte(backing.Provider + "\x00" + backing.ObjectID))
	return domain.MediaID("blackpearl-remote-" + hex.EncodeToString(digest[:16]))
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

// MediaPrefetcher is an optional scheduling capability implemented by sources
// that can stage bounded ranges without changing foreground open semantics.
type MediaPrefetcher interface {
	Prefetch(ctx context.Context, media domain.Media)
}

// Catalog orchestrates media metadata and cached bytes.
type Catalog struct {
	repository       Repository
	importer         POCImporter
	source           MediaSource
	prefetchMu       sync.Mutex
	prefetchedNextOf map[domain.MediaID]struct{}
}

// NewCatalog constructs a catalog service from its narrow boundaries.
func NewCatalog(repository Repository, importer POCImporter, source MediaSource) *Catalog {
	return &Catalog{
		repository:       repository,
		importer:         importer,
		source:           source,
		prefetchedNextOf: make(map[domain.MediaID]struct{}),
	}
}

// ImportPOC imports the legal synthetic fixture and persists its canonical catalog entry.
func (c *Catalog) ImportPOC(ctx context.Context, source string) (domain.Media, error) {
	ctx, span := otel.Tracer("blackpearl/core").Start(ctx, "catalog.import_poc")
	defer span.End()
	backing, size, err := c.importer.Import(ctx, source)
	if err != nil {
		return domain.Media{}, fmt.Errorf("import POC fixture: %w", err)
	}
	return c.persistPOC(ctx, backing, size)
}

// RegisterPOC persists the legal synthetic fixture as remote logical media
// without importing any source bytes into the local cache.
func (c *Catalog) RegisterPOC(ctx context.Context, backing domain.BackingRef, size int64) (domain.Media, error) {
	ctx, span := otel.Tracer("blackpearl/core").Start(ctx, "catalog.register_poc")
	defer span.End()
	if size <= 0 {
		return domain.Media{}, fmt.Errorf("POC logical size must be positive: %d", size)
	}
	validated, err := domain.NewBackingRef(backing.Provider, backing.ObjectID)
	if err != nil {
		return domain.Media{}, fmt.Errorf("validate POC backing: %w", err)
	}
	return c.persistPOC(ctx, validated, size)
}

func (c *Catalog) persistPOC(ctx context.Context, backing domain.BackingRef, size int64) (domain.Media, error) {
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
	c.prefetchNextEpisode(ctx, media)
	return reader, nil
}

func (c *Catalog) prefetchNextEpisode(ctx context.Context, current domain.Media) {
	prefetcher, supported := c.source.(MediaPrefetcher)
	if !supported || current.Type != domain.MediaTypeEpisode {
		return
	}
	c.prefetchMu.Lock()
	if _, exists := c.prefetchedNextOf[current.ID]; exists {
		c.prefetchMu.Unlock()
		return
	}
	c.prefetchedNextOf[current.ID] = struct{}{}
	c.prefetchMu.Unlock()

	items, err := c.repository.List(ctx)
	if err != nil {
		c.prefetchMu.Lock()
		delete(c.prefetchedNextOf, current.ID)
		c.prefetchMu.Unlock()
		return
	}
	next, found := nextEpisode(current, items)
	if !found {
		return
	}
	prefetcher.Prefetch(ctx, next)
}

func nextEpisode(current domain.Media, items []domain.Media) (domain.Media, bool) {
	parts := strings.Split(current.VirtualPath, "/")
	if len(parts) != 4 || parts[0] != "TV Shows" {
		return domain.Media{}, false
	}
	seriesPrefix := strings.Join(parts[:2], "/") + "/"
	var selected domain.Media
	for _, candidate := range items {
		if candidate.Type != domain.MediaTypeEpisode || candidate.VirtualPath <= current.VirtualPath || !strings.HasPrefix(candidate.VirtualPath, seriesPrefix) {
			continue
		}
		if selected.ID == "" || candidate.VirtualPath < selected.VirtualPath {
			selected = candidate
		}
	}
	return selected, selected.ID != ""
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
