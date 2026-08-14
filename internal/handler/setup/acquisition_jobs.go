package setup

import (
	"errors"
	"net/http"
	"strings"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/domain"
)

type acquisitionJobResponse struct {
	ID        string                         `json:"id"`
	State     acquisitiondomain.JobState     `json:"state"`
	MediaType domain.MediaType               `json:"mediaType"`
	Title     string                         `json:"title"`
	Year      int                            `json:"year"`
	Season    int                            `json:"season,omitempty"`
	Episode   int                            `json:"episode,omitempty"`
	Progress  int                            `json:"progress"`
	ErrorCode acquisitiondomain.JobErrorCode `json:"errorCode,omitempty"`
	CreatedAt time.Time                      `json:"createdAt"`
	UpdatedAt time.Time                      `json:"updatedAt"`
}

func (h *handler) serveAcquisitionJobs(writer http.ResponseWriter, request *http.Request) {
	if h.jobs == nil {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case http.MethodPost:
		h.submitAcquisitionJob(writer, request)
	case http.MethodGet:
		h.listAcquisitionJobs(writer, request)
	default:
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
	}
}

func (h *handler) submitAcquisitionJob(writer http.ResponseWriter, request *http.Request) {
	if !h.authorizeBrowser(request) {
		writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
		return
	}
	var input struct {
		MediaType domain.MediaType `json:"mediaType"`
		Title     string           `json:"title"`
		Year      int              `json:"year"`
		Season    int              `json:"season,omitempty"`
		Episode   int              `json:"episode,omitempty"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Enter a valid movie or episode request.")
		return
	}
	search, err := acquisitionRequest(input.MediaType, input.Title, input.Year, input.Season, input.Episode)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "invalid_intent", "Enter a valid movie or episode title, year, season, and episode.")
		return
	}
	if err := h.authorizeAcquisition(request); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	job, created, err := h.jobs.Submit(request.Context(), search)
	if err != nil {
		h.writeAcquisitionJobError(writer, request, err)
		return
	}
	if err := h.issueSession(writer, request, ""); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, struct {
		Job     acquisitionJobResponse `json:"job"`
		Created bool                   `json:"created"`
	}{Job: newAcquisitionJobResponse(job), Created: created})
}

func (h *handler) listAcquisitionJobs(writer http.ResponseWriter, request *http.Request) {
	if !h.authorizeBrowserRead(request) {
		writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
		return
	}
	if err := h.authorizeAcquisition(request); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	jobs, err := h.jobs.List(request.Context(), 20)
	if err != nil {
		h.writeAcquisitionJobError(writer, request, err)
		return
	}
	responses := make([]acquisitionJobResponse, 0, len(jobs))
	for _, job := range jobs {
		responses = append(responses, newAcquisitionJobResponse(job))
	}
	writeJSON(writer, http.StatusOK, struct {
		Jobs []acquisitionJobResponse `json:"jobs"`
	}{Jobs: responses})
}

func (h *handler) serveAcquisitionJob(writer http.ResponseWriter, request *http.Request) {
	if h.jobs == nil {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	if !h.authorizeBrowserRead(request) {
		writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
		return
	}
	if err := h.authorizeAcquisition(request); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/acquisition/jobs/")
	if id == "" || strings.Contains(id, "/") {
		writeError(writer, http.StatusNotFound, "job_not_found", "That background request was not found.")
		return
	}
	job, err := h.jobs.Get(request.Context(), id)
	if err != nil {
		h.writeAcquisitionJobError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, newAcquisitionJobResponse(job))
}

func newAcquisitionJobResponse(job acquisitiondomain.AcquisitionJob) acquisitionJobResponse {
	return acquisitionJobResponse{
		ID: job.ID(), State: job.State(), MediaType: job.Request().MediaType(),
		Title: job.Request().Title(), Year: job.Request().Year(),
		Season: job.Request().Season(), Episode: job.Request().Episode(),
		Progress: job.Progress(), ErrorCode: job.ErrorCode(),
		CreatedAt: job.CreatedAt(), UpdatedAt: job.UpdatedAt(),
	}
}

func (h *handler) writeAcquisitionJobError(writer http.ResponseWriter, request *http.Request, err error) {
	h.logger.WarnContext(request.Context(), "background acquisition request failed", "method", request.Method, "path", request.URL.Path, "error", err)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "job_not_found", "That background request was not found.")
		return
	}
	writeError(writer, http.StatusServiceUnavailable, "job_unavailable", "BlackPearl could not update background requests right now.")
}
