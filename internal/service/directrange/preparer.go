package directrange

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// Preparer verifies exact-file metadata without reading content bytes.
type Preparer struct {
	opener cache.RangeOpener
}

// NewPreparer validates the provider-neutral range opener dependency.
func NewPreparer(opener cache.RangeOpener) (*Preparer, error) {
	if opener == nil {
		return nil, errors.New("direct range opener is required")
	}
	return &Preparer{opener: opener}, nil
}

// Prepare verifies the selected exact file and returns its non-owned backing.
func (p *Preparer) Prepare(ctx context.Context, candidate acquisition.RangeCandidate) (acquisition.CreatedObject, error) {
	validated, err := acquisition.NewRangeCandidate(candidate.Media(), candidate.Indexer())
	if err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("validate direct range candidate: %w", err)
	}
	if err := p.verify(ctx, validated); err != nil {
		return acquisition.CreatedObject{}, err
	}
	backing := validated.Media().Backing()
	created, err := acquisition.NewCreatedObject(backing.Provider, backing.ObjectID)
	if err != nil {
		return acquisition.CreatedObject{}, fmt.Errorf("construct direct range object: %w", err)
	}
	return created, nil
}

// Inspect reopens a persisted exact-file selection and returns ready metadata.
func (p *Preparer) Inspect(
	ctx context.Context,
	selection acquisition.JobSelection,
	created acquisition.CreatedObject,
) (acquisition.PreparationInspection, error) {
	candidate, ok := selection.RangeCandidate()
	if !ok {
		return acquisition.PreparationInspection{}, errors.New("direct range inspection requires a range selection")
	}
	if candidate.Media().Backing() != created.Backing() {
		return acquisition.PreparationInspection{}, errors.New("direct range provider object does not match the selection")
	}
	if err := p.verify(ctx, candidate); err != nil {
		return acquisition.PreparationInspection{}, err
	}
	inspection, err := acquisition.NewPreparationInspection([]domain.MediaCandidate{candidate.Media()}, 100)
	if err != nil {
		return acquisition.PreparationInspection{}, fmt.Errorf("construct direct range inspection: %w", err)
	}
	return inspection, nil
}

func (p *Preparer) verify(ctx context.Context, candidate acquisition.RangeCandidate) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify direct range media: %w", err)
	}
	source, err := p.opener.Open(ctx, candidate.Media().Backing())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("open direct range media: %w", ctxErr)
		}
		return errors.New("open direct range media is unavailable")
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close direct range metadata"))
		}
	}()
	if source.Size() != candidate.Media().Size {
		return fmt.Errorf("direct range media size changed: got %d want %d", source.Size(), candidate.Media().Size)
	}
	if strings.TrimSpace(source.Validator()) == "" {
		return errors.New("direct range media requires an immutable validator")
	}
	return nil
}
