package directrange_test

import (
	"context"
	"io"
	"testing"

	"github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/cache"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/service/directrange"
	"github.com/stretchr/testify/require"
)

func TestPreparerUsesMetadataOnlyAndInspectsRestartSafeRangeSelection(t *testing.T) {
	t.Parallel()
	candidate := directCandidate(t, "object", "Example.Movie.2026.mp4", 16)
	source := &preparerSource{size: 16, validator: "sha1:fixture-object"}
	opener := &preparerOpener{source: source}
	preparer, err := directrange.NewPreparer(opener)
	require.NoError(t, err)

	created, err := preparer.Prepare(context.Background(), candidate)
	require.NoError(t, err)
	require.Equal(t, candidate.Media().Backing(), created.Backing())
	require.Zero(t, source.reads)
	require.True(t, source.closed)

	selection, err := acquisition.NewRangeJobSelection(candidate)
	require.NoError(t, err)
	opener.source = &preparerSource{size: 16, validator: "sha1:fixture-object"}
	inspection, err := preparer.Inspect(context.Background(), selection, created)
	require.NoError(t, err)
	require.Equal(t, 100, inspection.Progress())
	require.Equal(t, []domain.MediaCandidate{candidate.Media()}, inspection.Candidates())
	require.Zero(t, opener.source.reads)
}

func TestPreparerRejectsChangedObjectSizeOrMissingValidator(t *testing.T) {
	t.Parallel()
	candidate := directCandidate(t, "object", "Example.Movie.2026.mp4", 16)
	selection, err := acquisition.NewRangeJobSelection(candidate)
	require.NoError(t, err)

	for _, test := range []struct {
		name      string
		source    *preparerSource
		createdID string
	}{
		{name: "size", source: &preparerSource{size: 15, validator: "sha1:fixture-object"}, createdID: "object"},
		{name: "validator", source: &preparerSource{size: 16}, createdID: "object"},
		{name: "changed validator", source: &preparerSource{size: 16, validator: "sha1:replacement"}, createdID: "object"},
		{name: "object", source: &preparerSource{size: 16, validator: "sha1:fixture-object"}, createdID: "different"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			preparer, err := directrange.NewPreparer(&preparerOpener{source: test.source})
			require.NoError(t, err)
			created, err := acquisition.NewCreatedObject("internet-archive-file", test.createdID)
			require.NoError(t, err)

			_, err = preparer.Inspect(context.Background(), selection, created)

			require.ErrorIs(t, err, acquisition.ErrRangeUnplayable)
		})
	}
}

func TestPreparerValidatesDependenciesVariantsAndCancellation(t *testing.T) {
	t.Parallel()
	_, err := directrange.NewPreparer(nil)
	require.Error(t, err)
	preparer, err := directrange.NewPreparer(&preparerOpener{source: &preparerSource{size: 16, validator: "valid"}})
	require.NoError(t, err)
	created, err := acquisition.NewCreatedObject("internet-archive-file", "object")
	require.NoError(t, err)

	_, err = preparer.Inspect(context.Background(), acquisition.JobSelection{}, created)
	require.Error(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = preparer.Prepare(canceled, directCandidate(t, "object", "Example.Movie.2026.mp4", 16))
	require.ErrorIs(t, err, context.Canceled)

	notFound, err := directrange.NewPreparer(&preparerOpener{err: domain.ErrNotFound})
	require.NoError(t, err)
	_, err = notFound.Prepare(context.Background(), directCandidate(t, "missing", "Missing.Movie.2026.mp4", 16))
	require.ErrorIs(t, err, domain.ErrNotFound)
}

type preparerOpener struct {
	source *preparerSource
	err    error
}

func (o *preparerOpener) Open(ctx context.Context, _ domain.BackingRef) (acquisition.RangeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.err != nil {
		return nil, o.err
	}
	return o.source, nil
}

func (o *preparerOpener) Ready(context.Context) error { return nil }

type preparerSource struct {
	size      int64
	validator string
	reads     int
	closed    bool
}

func (s *preparerSource) ReadAt(context.Context, []byte, int64) (int, error) {
	s.reads++
	return 0, io.EOF
}
func (s *preparerSource) Size() int64       { return s.size }
func (s *preparerSource) Validator() string { return s.validator }
func (s *preparerSource) Close() error {
	s.closed = true
	return nil
}

var _ cache.RangeOpener = (*preparerOpener)(nil)
