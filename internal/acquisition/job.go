package acquisition

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/kurtnissen/blackpearl/internal/domain"
)

var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// MaximumJobCandidates caps automatic provider mutations for one durable job.
const MaximumJobCandidates = 5

const maximumRangeValidatorBytes = 512

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

// SelectionKind identifies the durable acquisition path for one candidate.
type SelectionKind string

const (
	// SelectionKindTorrent identifies a locator-free BitTorrent release.
	SelectionKindTorrent SelectionKind = "torrent"
	// SelectionKindRange identifies an exact provider-backed logical media file.
	SelectionKindRange SelectionKind = "range"
)

// RangeCandidate is safe, durable metadata for one exact range-readable file.
// Its backing object ID is provider-opaque and must not contain a source URL.
type RangeCandidate struct {
	media     domain.MediaCandidate
	indexer   string
	validator string
}

// NewRangeCandidate validates one exact provider-backed media file.
func NewRangeCandidate(media domain.MediaCandidate, indexer string, validator string) (RangeCandidate, error) {
	validated, err := domain.NewProviderMediaCandidate(media.Backing(), media.Name, media.Size)
	if err != nil {
		return RangeCandidate{}, fmt.Errorf("validate range media candidate: %w", err)
	}
	if media.Extension != "" && media.Extension != validated.Extension {
		return RangeCandidate{}, errors.New("range media candidate extension does not match its name")
	}
	cleanIndexer, err := validateSearchText("range candidate indexer", indexer, maximumIndexerNameBytes)
	if err != nil {
		return RangeCandidate{}, err
	}
	cleanValidator, err := validateRangeValidator(validator)
	if err != nil {
		return RangeCandidate{}, err
	}
	return RangeCandidate{media: validated, indexer: cleanIndexer, validator: cleanValidator}, nil
}

func validateRangeValidator(value string) (string, error) {
	clean := strings.TrimSpace(value)
	if clean == "" || clean != value {
		return "", errors.New("range candidate validator requires non-whitespace text without surrounding whitespace")
	}
	if len(clean) > maximumRangeValidatorBytes {
		return "", fmt.Errorf("range candidate validator must not exceed %d bytes", maximumRangeValidatorBytes)
	}
	for _, character := range clean {
		if unicode.IsControl(character) {
			return "", errors.New("range candidate validator must not contain control characters")
		}
	}
	return clean, nil
}

func (c RangeCandidate) isZero() bool {
	return c == (RangeCandidate{})
}

// Media returns provider-neutral exact-file metadata.
func (c RangeCandidate) Media() domain.MediaCandidate { return c.media }

// Indexer returns the safe catalog or index display name.
func (c RangeCandidate) Indexer() string { return c.indexer }

// Validator returns the immutable content fingerprint selected by the resolver.
func (c RangeCandidate) Validator() string { return c.validator }

// JobSelection is the safe, durable union of torrent and exact-file candidates.
// It intentionally contains no source URL, magnet, download URL, or credential.
type JobSelection struct {
	kind           SelectionKind
	release        Release
	rangeCandidate RangeCandidate
}

// CandidateOutcome is the public-safe durable state of one release candidate.
type CandidateOutcome string

const (
	CandidateOutcomePending    CandidateOutcome = "pending"
	CandidateOutcomeSelected   CandidateOutcome = "selected"
	CandidateOutcomeStalled    CandidateOutcome = "stalled"
	CandidateOutcomeMissing    CandidateOutcome = "missing"
	CandidateOutcomeUnplayable CandidateOutcome = "unplayable"
)

// JobCandidate is one locator-free release in a bounded durable fallback plan.
type JobCandidate struct {
	selection JobSelection
	ordinal   int
	outcome   CandidateOutcome
}

// NewJobCandidate validates one ordered durable release candidate.
func NewJobCandidate(selection JobSelection, ordinal int, outcome CandidateOutcome) (JobCandidate, error) {
	if !selection.valid() {
		return JobCandidate{}, errors.New("acquisition job candidate requires a valid selection")
	}
	if ordinal < 0 || ordinal >= MaximumJobCandidates {
		return JobCandidate{}, fmt.Errorf("acquisition job candidate ordinal must be between 0 and %d", MaximumJobCandidates-1)
	}
	switch outcome {
	case CandidateOutcomePending, CandidateOutcomeSelected, CandidateOutcomeStalled,
		CandidateOutcomeMissing, CandidateOutcomeUnplayable:
	default:
		return JobCandidate{}, fmt.Errorf("unsupported acquisition job candidate outcome: %q", outcome)
	}
	validated, err := cloneJobSelection(selection)
	if err != nil {
		return JobCandidate{}, fmt.Errorf("validate acquisition job candidate selection: %w", err)
	}
	return JobCandidate{selection: validated, ordinal: ordinal, outcome: outcome}, nil
}

// Selection returns locator-free release metadata.
func (c JobCandidate) Selection() JobSelection { return c.selection }

// Ordinal returns the zero-based fallback position.
func (c JobCandidate) Ordinal() int { return c.ordinal }

// Outcome returns the public-safe candidate state.
func (c JobCandidate) Outcome() CandidateOutcome { return c.outcome }

// NewJobSelection strips ephemeral locators from a validated torrent release.
func NewJobSelection(release Release) (JobSelection, error) {
	return NewTorrentJobSelection(release)
}

// NewTorrentJobSelection strips ephemeral locators from a validated torrent release.
func NewTorrentJobSelection(release Release) (JobSelection, error) {
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
	return JobSelection{kind: SelectionKindTorrent, release: safe}, nil
}

// NewRangeJobSelection stores exact-file metadata without a source URL.
func NewRangeJobSelection(candidate RangeCandidate) (JobSelection, error) {
	validated, err := NewRangeCandidate(candidate.Media(), candidate.Indexer(), candidate.Validator())
	if err != nil {
		return JobSelection{}, fmt.Errorf("validate durable range selection: %w", err)
	}
	return JobSelection{kind: SelectionKindRange, rangeCandidate: validated}, nil
}

func (s JobSelection) valid() bool {
	_, err := cloneJobSelection(s)
	return err == nil
}

func cloneJobSelection(selection JobSelection) (JobSelection, error) {
	switch selection.kind {
	case SelectionKindTorrent:
		if !selection.rangeCandidate.isZero() {
			return JobSelection{}, errors.New("torrent selection cannot contain range metadata")
		}
		return NewTorrentJobSelection(selection.release)
	case SelectionKindRange:
		if selection.release != (Release{}) {
			return JobSelection{}, errors.New("range selection cannot contain torrent metadata")
		}
		return NewRangeJobSelection(selection.rangeCandidate)
	default:
		return JobSelection{}, fmt.Errorf("unsupported acquisition selection kind: %q", selection.kind)
	}
}

// Release reconstructs the locator-free release metadata used for publication.
func (s JobSelection) Release() Release { return s.release }

// Kind returns the durable acquisition variant.
func (s JobSelection) Kind() SelectionKind { return s.kind }

// TorrentRelease returns torrent metadata only for torrent selections.
func (s JobSelection) TorrentRelease() (Release, bool) {
	if s.kind != SelectionKindTorrent {
		return Release{}, false
	}
	return s.release, true
}

// RangeCandidate returns exact-file metadata only for range selections.
func (s JobSelection) RangeCandidate() (RangeCandidate, bool) {
	if s.kind != SelectionKindRange {
		return RangeCandidate{}, false
	}
	return s.rangeCandidate, true
}

// Identity returns the provider-local stable selection identity.
func (s JobSelection) Identity() string {
	if s.kind == SelectionKindRange {
		return s.rangeCandidate.Media().ObjectID
	}
	return s.release.InfoHash()
}

// Provider returns the search provider that produced the release.
func (s JobSelection) Provider() string {
	if s.kind == SelectionKindRange {
		return s.rangeCandidate.Media().Backing().Provider
	}
	return s.release.Provider()
}

// Title returns the safe provider release title.
func (s JobSelection) Title() string {
	if s.kind == SelectionKindRange {
		return s.rangeCandidate.Media().Name
	}
	return s.release.Title()
}

// Size returns the provider-advertised release size.
func (s JobSelection) Size() int64 {
	if s.kind == SelectionKindRange {
		return s.rangeCandidate.Media().Size
	}
	return s.release.Size()
}

// Indexer returns the safe indexer display name.
func (s JobSelection) Indexer() string {
	if s.kind == SelectionKindRange {
		return s.rangeCandidate.Indexer()
	}
	return s.release.Indexer()
}

// InfoHash returns the stable BitTorrent content fingerprint.
func (s JobSelection) InfoHash() string { return s.release.InfoHash() }

// Seeders returns the provider-reported seed count.
func (s JobSelection) Seeders() int { return s.release.Seeders() }

// HasSeeders reports whether the selected release included a seed count.
func (s JobSelection) HasSeeders() bool { return s.release.HasSeeders() }

// JobSnapshotInput is the persistence-boundary representation used to validate
// a durable acquisition job loaded from storage.
type JobSnapshotInput struct {
	ID                       string
	Request                  SearchRequest
	State                    JobState
	Selection                *JobSelection
	CreatedObject            *CreatedObject
	SelectedCandidateOrdinal *int
	CreatedByJob             bool
	PublishedObjectID        string
	ErrorCode                JobErrorCode
	Attempt                  int
	Progress                 int
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// AcquisitionJob is one immutable, privacy-safe durable job snapshot.
type AcquisitionJob struct {
	id                       string
	request                  SearchRequest
	state                    JobState
	selection                JobSelection
	hasSelection             bool
	createdObject            CreatedObject
	hasCreatedObject         bool
	selectedCandidateOrdinal int
	hasCandidatePlan         bool
	createdByJob             bool
	publishedObjectID        string
	errorCode                JobErrorCode
	attempt                  int
	progress                 int
	createdAt                time.Time
	updatedAt                time.Time
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
	selectedOrdinal, hasCandidatePlan, err := validateSelectedCandidateOrdinal(input.State, hasSelection, input.SelectedCandidateOrdinal)
	if err != nil {
		return AcquisitionJob{}, err
	}
	if err := validateCreatedByJob(input.State, hasCreated, input.CreatedByJob); err != nil {
		return AcquisitionJob{}, err
	}
	return AcquisitionJob{
		id: input.ID, request: request, state: input.State,
		selection: selection, hasSelection: hasSelection,
		createdObject: created, hasCreatedObject: hasCreated,
		selectedCandidateOrdinal: selectedOrdinal, hasCandidatePlan: hasCandidatePlan,
		createdByJob:      input.CreatedByJob,
		publishedObjectID: published, errorCode: input.ErrorCode,
		attempt: input.Attempt, progress: input.Progress,
		createdAt: input.CreatedAt.UTC(), updatedAt: input.UpdatedAt.UTC(),
	}, nil
}

func validateSelectedCandidateOrdinal(state JobState, hasSelection bool, ordinal *int) (int, bool, error) {
	if ordinal == nil {
		return 0, false, nil
	}
	if *ordinal < 0 || *ordinal >= MaximumJobCandidates {
		return 0, false, fmt.Errorf("selected acquisition candidate ordinal must be between 0 and %d", MaximumJobCandidates-1)
	}
	if !hasSelection {
		return 0, false, errors.New("selected acquisition candidate requires a release selection")
	}
	switch state {
	case JobStateSelected, JobStatePreparing, JobStateSucceeded, JobStateManualReview:
		return *ordinal, true, nil
	default:
		return 0, false, errors.New("acquisition job stage cannot contain a selected candidate ordinal")
	}
}

func validateCreatedByJob(state JobState, hasCreated bool, createdByJob bool) error {
	if !createdByJob {
		return nil
	}
	if !hasCreated {
		return errors.New("BlackPearl-created provenance requires a provider object")
	}
	switch state {
	case JobStatePreparing, JobStateSucceeded, JobStateManualReview:
		return nil
	default:
		return errors.New("acquisition job stage cannot own a provider object")
	}
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
	validated, err := cloneJobSelection(*input)
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

// SelectedCandidateOrdinal returns the current fallback position and whether
// this job owns a durable candidate plan. Legacy jobs return false.
func (j AcquisitionJob) SelectedCandidateOrdinal() (int, bool) {
	return j.selectedCandidateOrdinal, j.hasCandidatePlan
}

// CreatedByJob reports whether BlackPearl created the attached provider object.
func (j AcquisitionJob) CreatedByJob() bool { return j.createdByJob }

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
