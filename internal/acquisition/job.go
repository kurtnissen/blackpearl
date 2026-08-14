package acquisition

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// JobState is the durable lifecycle stage of one explicitly requested
// background acquisition.
type JobState string

const (
	JobStateQueued       JobState = "queued"
	JobStateSelected     JobState = "selected"
	JobStatePreparing    JobState = "preparing"
	JobStateSucceeded    JobState = "succeeded"
	JobStateFailed       JobState = "failed"
	JobStateManualReview JobState = "manual_review"
)

// JobErrorCode is a public-safe durable failure classification. Provider error
// details and credentials must never be stored in this value.
type JobErrorCode string

const (
	JobErrorNone                JobErrorCode = ""
	JobErrorProviderUnavailable JobErrorCode = "provider_unavailable"
	JobErrorUnauthorized        JobErrorCode = "unauthorized"
	JobErrorNoRelease           JobErrorCode = "no_release"
	JobErrorNoPlayableMedia     JobErrorCode = "no_playable_media"
	JobErrorMaterialization     JobErrorCode = "materialization_failed"
	JobErrorStalled             JobErrorCode = "stalled"
	JobErrorAmbiguousMutation   JobErrorCode = "ambiguous_mutation"
)

// JobSelection is the safe, durable subset of one ephemeral provider release.
// It intentionally contains no source URL, magnet, download URL, or credential.
type JobSelection struct {
	release Release
}

// NewJobSelection strips ephemeral locators from a validated torrent release.
func NewJobSelection(release Release) (JobSelection, error) {
	if release.Protocol() != ReleaseProtocolTorrent || release.InfoHash() == "" {
		return JobSelection{}, errors.New("durable acquisition selection requires a torrent info hash")
	}
	var seeders *int
	if release.HasSeeders() {
		value := release.Seeders()
		seeders = &value
	}
	safe, err := NewRelease(ReleaseInput{
		Provider: release.Provider(), SourceID: "torrent:" + release.InfoHash(),
		Title: release.Title(), Protocol: release.Protocol(), Size: release.Size(),
		Indexer: release.Indexer(), InfoHash: release.InfoHash(), Seeders: seeders,
	})
	if err != nil {
		return JobSelection{}, fmt.Errorf("validate durable acquisition selection: %w", err)
	}
	return JobSelection{release: safe}, nil
}

func (s JobSelection) valid() bool {
	_, err := NewJobSelection(s.release)
	return err == nil
}

// Release reconstructs the locator-free release metadata used for publication.
func (s JobSelection) Release() Release { return s.release }

// Provider returns the search provider that produced the release.
func (s JobSelection) Provider() string { return s.release.Provider() }

// Title returns the safe provider release title.
func (s JobSelection) Title() string { return s.release.Title() }

// Size returns the provider-advertised release size.
func (s JobSelection) Size() int64 { return s.release.Size() }

// Indexer returns the safe indexer display name.
func (s JobSelection) Indexer() string { return s.release.Indexer() }

// InfoHash returns the stable BitTorrent content fingerprint.
func (s JobSelection) InfoHash() string { return s.release.InfoHash() }

// Seeders returns the provider-reported seed count.
func (s JobSelection) Seeders() int { return s.release.Seeders() }

// HasSeeders reports whether the selected release included a seed count.
func (s JobSelection) HasSeeders() bool { return s.release.HasSeeders() }

// JobSnapshotInput is the persistence-boundary representation used to validate
// a durable acquisition job loaded from storage.
type JobSnapshotInput struct {
	ID                string
	Request           SearchRequest
	State             JobState
	Selection         *JobSelection
	CreatedObject     *CreatedObject
	PublishedObjectID string
	ErrorCode         JobErrorCode
	Attempt           int
	Progress          int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// AcquisitionJob is one immutable, privacy-safe durable job snapshot.
type AcquisitionJob struct {
	id                string
	request           SearchRequest
	state             JobState
	selection         JobSelection
	hasSelection      bool
	createdObject     CreatedObject
	hasCreatedObject  bool
	publishedObjectID string
	errorCode         JobErrorCode
	attempt           int
	progress          int
	createdAt         time.Time
	updatedAt         time.Time
}

// NewAcquisitionJobSnapshot validates a job loaded from or written to durable
// storage.
func NewAcquisitionJobSnapshot(input JobSnapshotInput) (AcquisitionJob, error) {
	if !jobIDPattern.MatchString(input.ID) {
		return AcquisitionJob{}, errors.New("acquisition job ID must be 32 lowercase hexadecimal characters")
	}
	request, err := validateSearchRequest(input.Request)
	if err != nil {
		return AcquisitionJob{}, fmt.Errorf("validate acquisition job request: %w", err)
	}
	if input.Attempt < 0 {
		return AcquisitionJob{}, errors.New("acquisition job attempt must not be negative")
	}
	if input.Progress < 0 || input.Progress > 100 {
		return AcquisitionJob{}, errors.New("acquisition job progress must be between 0 and 100")
	}
	if input.CreatedAt.IsZero() || input.UpdatedAt.IsZero() || input.UpdatedAt.Before(input.CreatedAt) {
		return AcquisitionJob{}, errors.New("acquisition job timestamps are invalid")
	}
	if err := validateJobErrorCode(input.ErrorCode); err != nil {
		return AcquisitionJob{}, err
	}
	selection, hasSelection, err := validateJobSelection(input.Selection)
	if err != nil {
		return AcquisitionJob{}, err
	}
	created, hasCreated, err := validateJobCreatedObject(input.CreatedObject)
	if err != nil {
		return AcquisitionJob{}, err
	}
	published, err := validateOptionalPublishedObjectID(input.PublishedObjectID)
	if err != nil {
		return AcquisitionJob{}, err
	}
	if err := validateJobStage(input.State, hasSelection, hasCreated, published, input.ErrorCode, input.Progress); err != nil {
		return AcquisitionJob{}, err
	}
	return AcquisitionJob{
		id: input.ID, request: request, state: input.State,
		selection: selection, hasSelection: hasSelection,
		createdObject: created, hasCreatedObject: hasCreated,
		publishedObjectID: published, errorCode: input.ErrorCode,
		attempt: input.Attempt, progress: input.Progress,
		createdAt: input.CreatedAt.UTC(), updatedAt: input.UpdatedAt.UTC(),
	}, nil
}

func validateJobErrorCode(code JobErrorCode) error {
	switch code {
	case JobErrorNone, JobErrorProviderUnavailable, JobErrorUnauthorized, JobErrorNoRelease,
		JobErrorNoPlayableMedia, JobErrorMaterialization, JobErrorStalled, JobErrorAmbiguousMutation:
		return nil
	default:
		return fmt.Errorf("unsupported acquisition job error code: %q", code)
	}
}

func validateJobSelection(input *JobSelection) (JobSelection, bool, error) {
	if input == nil {
		return JobSelection{}, false, nil
	}
	if !input.valid() {
		return JobSelection{}, false, errors.New("acquisition job selection is invalid")
	}
	validated, err := NewJobSelection(input.release)
	if err != nil {
		return JobSelection{}, false, fmt.Errorf("validate acquisition job selection: %w", err)
	}
	return validated, true, nil
}

func validateJobCreatedObject(input *CreatedObject) (CreatedObject, bool, error) {
	if input == nil {
		return CreatedObject{}, false, nil
	}
	validated, err := NewCreatedObject(input.Provider(), input.ObjectID())
	if err != nil {
		return CreatedObject{}, false, fmt.Errorf("validate acquisition job provider object: %w", err)
	}
	return validated, true, nil
}

func validateOptionalPublishedObjectID(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("published object ID must not contain control characters")
		}
	}
	clean := strings.TrimSpace(value)
	if clean == "" || clean != value {
		return "", errors.New("published object ID requires non-whitespace text without surrounding whitespace")
	}
	if len(clean) > maximumPublishedObjectIDBytes {
		return "", fmt.Errorf("published object ID must not exceed %d bytes", maximumPublishedObjectIDBytes)
	}
	return clean, nil
}

func validateJobStage(state JobState, hasSelection bool, hasCreated bool, published string, code JobErrorCode, progress int) error {
	switch state {
	case JobStateQueued:
		if hasSelection || hasCreated || published != "" || progress != 0 {
			return errors.New("queued acquisition job cannot contain provider results")
		}
	case JobStateSelected:
		if !hasSelection || hasCreated || published != "" || progress != 0 {
			return errors.New("selected acquisition job requires only a durable release selection")
		}
	case JobStatePreparing:
		if !hasSelection || !hasCreated || published != "" || progress >= 100 {
			return errors.New("preparing acquisition job requires an unfinished provider object")
		}
	case JobStateSucceeded:
		if !hasSelection || !hasCreated || published == "" || progress != 100 || code != JobErrorNone {
			return errors.New("succeeded acquisition job requires a published provider object")
		}
	case JobStateFailed:
		if published != "" || code == JobErrorNone {
			return errors.New("failed acquisition job requires a public-safe error code")
		}
	case JobStateManualReview:
		if published != "" || code != JobErrorAmbiguousMutation {
			return errors.New("manual-review acquisition job requires an ambiguous-mutation code")
		}
	default:
		return fmt.Errorf("unsupported acquisition job state: %q", state)
	}
	return nil
}

// ID returns the public random job identifier.
func (j AcquisitionJob) ID() string { return j.id }

// Request returns the validated media intent.
func (j AcquisitionJob) Request() SearchRequest { return j.request }

// State returns the durable lifecycle stage.
func (j AcquisitionJob) State() JobState { return j.state }

// HasSelection reports whether a stable release has been chosen.
func (j AcquisitionJob) HasSelection() bool { return j.hasSelection }

// Selection returns the chosen release metadata, or a zero value when absent.
func (j AcquisitionJob) Selection() JobSelection { return j.selection }

// HasCreatedObject reports whether the provider object ID is durable.
func (j AcquisitionJob) HasCreatedObject() bool { return j.hasCreatedObject }

// CreatedObject returns the provider object, or a zero value when absent.
func (j AcquisitionJob) CreatedObject() CreatedObject { return j.createdObject }

// PublishedObjectID returns the range-readable media object after success.
func (j AcquisitionJob) PublishedObjectID() string { return j.publishedObjectID }

// ErrorCode returns a privacy-safe status classification.
func (j AcquisitionJob) ErrorCode() JobErrorCode { return j.errorCode }

// Attempt returns the number of acquired worker leases.
func (j AcquisitionJob) Attempt() int { return j.attempt }

// Progress returns advisory preparation progress in the range 0-100.
func (j AcquisitionJob) Progress() int { return j.progress }

// CreatedAt returns the UTC submission time.
func (j AcquisitionJob) CreatedAt() time.Time { return j.createdAt }

// UpdatedAt returns the UTC last-transition time.
func (j AcquisitionJob) UpdatedAt() time.Time { return j.updatedAt }

// AcquisitionJobClaim is one immutable, versioned worker lease.
type AcquisitionJobClaim struct {
	job          AcquisitionJob
	leaseVersion int64
}

// NewAcquisitionJobClaim validates a leased job snapshot.
func NewAcquisitionJobClaim(job AcquisitionJob, leaseVersion int64) (AcquisitionJobClaim, error) {
	if job.ID() == "" {
		return AcquisitionJobClaim{}, errors.New("acquisition job claim requires a validated job")
	}
	if leaseVersion < 1 {
		return AcquisitionJobClaim{}, errors.New("acquisition job claim lease version must be positive")
	}
	if job.State() == JobStateSucceeded || job.State() == JobStateFailed || job.State() == JobStateManualReview {
		return AcquisitionJobClaim{}, errors.New("terminal acquisition job cannot be leased")
	}
	return AcquisitionJobClaim{job: job, leaseVersion: leaseVersion}, nil
}

// Job returns the leased durable snapshot.
func (c AcquisitionJobClaim) Job() AcquisitionJob { return c.job }

// LeaseVersion returns the optimistic-concurrency token.
func (c AcquisitionJobClaim) LeaseVersion() int64 { return c.leaseVersion }
