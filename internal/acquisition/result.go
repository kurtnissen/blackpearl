package acquisition

import (
	"errors"
	"fmt"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

var (
	// ErrNotReady indicates that a created provider object exists but is not yet
	// available as complete, range-readable media.
	ErrNotReady = errors.New("acquired object is not ready")
	// ErrAmbiguousProviderObjects indicates that reconciliation found multiple
	// account objects for one stable content fingerprint.
	ErrAmbiguousProviderObjects = errors.New("multiple provider objects match the selected content")
)

// CreatedObject identifies one provider account object created by an
// acquisition gateway. It is not necessarily a directly readable media file.
type CreatedObject struct {
	backing domain.BackingRef
}

// NewCreatedObject constructs a provider-neutral account object reference.
func NewCreatedObject(provider string, objectID string) (CreatedObject, error) {
	backing, err := domain.NewBackingRef(provider, objectID)
	if err != nil {
		return CreatedObject{}, fmt.Errorf("validate created object: %w", err)
	}
	return CreatedObject{backing: backing}, nil
}

// Provider returns the acquisition provider name.
func (o CreatedObject) Provider() string { return o.backing.Provider }

// ObjectID returns the provider-local account object identifier.
func (o CreatedObject) ObjectID() string { return o.backing.ObjectID }

// Backing returns an independent provider-neutral reference value.
func (o CreatedObject) Backing() domain.BackingRef { return o.backing }

// AcquiredMedia binds validated search intent to the exact torrent release and
// eligible provider media candidate selected from the created account object.
type AcquiredMedia struct {
	request   SearchRequest
	release   Release
	candidate domain.MediaCandidate
}

// NewAcquiredMedia constructs an immutable cached-torrent acquisition result.
func NewAcquiredMedia(request SearchRequest, release Release, candidate domain.MediaCandidate) (AcquiredMedia, error) {
	validatedRequest, err := validateSearchRequest(request)
	if err != nil {
		return AcquiredMedia{}, err
	}
	if release.Protocol() != ReleaseProtocolTorrent || release.Provider() == "" || release.SourceID() == "" || release.InfoHash() == "" {
		return AcquiredMedia{}, errors.New("acquired media requires a validated torrent release with info hash")
	}
	validatedCandidate, err := domain.NewMediaCandidate(candidate.ObjectID, candidate.Name, candidate.Size)
	if err != nil {
		return AcquiredMedia{}, fmt.Errorf("validate acquired media candidate: %w", err)
	}
	if candidate.Extension != "" && candidate.Extension != validatedCandidate.Extension {
		return AcquiredMedia{}, errors.New("acquired media candidate extension does not match its name")
	}
	return AcquiredMedia{request: validatedRequest, release: release, candidate: validatedCandidate}, nil
}

func validateSearchRequest(request SearchRequest) (SearchRequest, error) {
	switch request.MediaType() {
	case domain.MediaTypeMovie:
		return NewMovieSearch(request.Title(), request.Year())
	case domain.MediaTypeEpisode:
		return NewEpisodeSearch(request.Title(), request.Year(), request.Season(), request.Episode())
	default:
		return SearchRequest{}, errors.New("acquired media requires validated search intent")
	}
}

// Request returns the validated movie or episode intent.
func (m AcquiredMedia) Request() SearchRequest { return m.request }

// Release returns the exact cached torrent release used for acquisition.
func (m AcquiredMedia) Release() Release { return m.release }

// Candidate returns the validated provider media candidate.
func (m AcquiredMedia) Candidate() domain.MediaCandidate { return m.candidate }
