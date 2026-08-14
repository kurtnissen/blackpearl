package setup

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
	watchlistservice "github.com/blackpearl-media/blackpearl/internal/service/watchlist"
)

// AcquisitionService is the private configuration and acquisition boundary
// consumed by paired HTTP routes.
type AcquisitionService interface {
	Status(ctx context.Context) (acquisitionservice.CoordinatorStatus, error)
	Configure(ctx context.Context, endpoint string, credential string) error
	Acquire(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error)
}

// AcquisitionJobService is the durable background-job boundary consumed by
// paired HTTP routes.
type AcquisitionJobService interface {
	Submit(ctx context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquisitionJob, bool, error)
	Get(ctx context.Context, id string) (acquisitiondomain.AcquisitionJob, error)
	List(ctx context.Context, limit int) ([]acquisitiondomain.AcquisitionJob, error)
}

// WatchlistService returns privacy-safe observation state to a paired browser.
type WatchlistService interface {
	Status(ctx context.Context) (watchlistservice.ObserverStatus, error)
}

// NewWithAcquisition constructs setup and acquisition routes with one shared
// process-local CSRF and browser-pairing boundary.
func NewWithAcquisition(service Service, acquisition AcquisitionService, configuredLogger ...*slog.Logger) (http.Handler, error) {
	if acquisition == nil {
		return nil, errors.New("acquisition service is required")
	}
	return newHandler(service, acquisition, configuredLogger...)
}

// NewWithAcquisitionAndJobs constructs setup, instant acquisition, and durable
// background acquisition routes behind one pairing boundary.
func NewWithAcquisitionAndJobs(
	service Service,
	acquisition AcquisitionService,
	jobs AcquisitionJobService,
	configuredLogger ...*slog.Logger,
) (http.Handler, error) {
	if acquisition == nil || jobs == nil {
		return nil, errors.New("acquisition and job services are required")
	}
	configured, err := newHandler(service, acquisition, configuredLogger...)
	if err != nil {
		return nil, err
	}
	configured.jobs = jobs
	return configured, nil
}

// NewWithAcquisitionAndWatchlist constructs the paired setup, acquisition, and
// observe-only watchlist API.
func NewWithAcquisitionAndWatchlist(
	service Service,
	acquisition AcquisitionService,
	watchlist WatchlistService,
	configuredLogger ...*slog.Logger,
) (http.Handler, error) {
	if acquisition == nil || watchlist == nil {
		return nil, errors.New("acquisition and watchlist services are required")
	}
	configured, err := newHandler(service, acquisition, configuredLogger...)
	if err != nil {
		return nil, err
	}
	configured.watchlist = watchlist
	return configured, nil
}

// NewWithAcquisitionJobsAndWatchlist constructs the complete paired control
// API used by the browser-setup runtime.
func NewWithAcquisitionJobsAndWatchlist(
	service Service,
	acquisition AcquisitionService,
	jobs AcquisitionJobService,
	watchlist WatchlistService,
	configuredLogger ...*slog.Logger,
) (http.Handler, error) {
	if acquisition == nil || jobs == nil || watchlist == nil {
		return nil, errors.New("acquisition, job, and watchlist services are required")
	}
	configured, err := newHandler(service, acquisition, configuredLogger...)
	if err != nil {
		return nil, err
	}
	configured.jobs = jobs
	configured.watchlist = watchlist
	return configured, nil
}

func (h *handler) serveWatchlistStatus(writer http.ResponseWriter, request *http.Request) {
	if h.watchlist == nil {
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
	status, err := h.watchlist.Status(request.Context())
	if err != nil {
		h.logger.WarnContext(request.Context(), "watchlist status failed", "error", err)
		if errors.Is(err, domain.ErrUnauthorized) {
			writeError(writer, http.StatusUnauthorized, "plex_unauthorized", "Plex rejected the saved watchlist credential. Sign in to Plex again and retry.")
			return
		}
		writeError(writer, http.StatusServiceUnavailable, "watchlist_unavailable", "Plex watchlist status is temporarily unavailable.")
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (h *handler) serveAcquisitionStatus(writer http.ResponseWriter, request *http.Request) {
	if h.acquisition == nil {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	status, err := h.acquisition.Status(request.Context())
	if err != nil {
		h.writeAcquisitionError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (h *handler) serveAcquisitionSettings(writer http.ResponseWriter, request *http.Request) {
	if h.acquisition == nil {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, http.MethodPut)
		return
	}
	if !h.authorizeBrowser(request) {
		writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
		return
	}
	var input struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Enter valid Prowlarr connection details.")
		return
	}
	if err := h.authorizeAcquisition(request); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	if err := h.acquisition.Configure(request.Context(), input.BaseURL, input.APIKey); err != nil {
		h.writeAcquisitionError(writer, request, err)
		return
	}
	if err := h.issueSession(writer, request, ""); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, acquisitionservice.CoordinatorStatus{Configured: true})
}

func (h *handler) serveAcquisition(writer http.ResponseWriter, request *http.Request) {
	if h.acquisition == nil {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
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
	media, err := h.acquisition.Acquire(request.Context(), search)
	if err != nil {
		h.writeAcquisitionError(writer, request, err)
		return
	}
	selectedItems := h.service.Status().SelectedItems
	if len(selectedItems) == 0 {
		if selected := h.service.Status().Selected; selected != nil {
			selectedItems = []domain.SetupConfiguration{*selected}
		}
	}
	selected, found := findSelectedConfiguration(selectedItems, media.Candidate().ObjectID)
	if !found {
		h.writeAcquisitionError(writer, request, acquisitionservice.ErrUnavailable)
		return
	}
	if err := h.issueSession(writer, request, ""); err != nil {
		h.writeServiceError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Selected      domain.SetupConfiguration   `json:"selected"`
		SelectedItems []domain.SetupConfiguration `json:"selectedItems"`
	}{Selected: selected, SelectedItems: selectedItems})
}

func (h *handler) authorizeAcquisition(request *http.Request) error {
	return h.service.AuthorizeSetup(
		request.Context(), "", request.Header.Get(setupSessionHeader), request.Header.Get(setupBootstrapHeader),
	)
}

func acquisitionRequest(mediaType domain.MediaType, title string, year int, season int, episode int) (acquisitiondomain.SearchRequest, error) {
	switch mediaType {
	case domain.MediaTypeMovie:
		if season != 0 || episode != 0 {
			return acquisitiondomain.SearchRequest{}, errors.New("movie intent cannot include episode coordinates")
		}
		return acquisitiondomain.NewMovieSearch(title, year)
	case domain.MediaTypeEpisode:
		return acquisitiondomain.NewEpisodeSearch(title, year, season, episode)
	default:
		return acquisitiondomain.SearchRequest{}, errors.New("unsupported acquisition media type")
	}
}

func findSelectedConfiguration(items []domain.SetupConfiguration, objectID string) (domain.SetupConfiguration, bool) {
	for _, item := range items {
		if item.ObjectID == objectID {
			return item, true
		}
	}
	return domain.SetupConfiguration{}, false
}

func (h *handler) writeAcquisitionError(writer http.ResponseWriter, request *http.Request, err error) {
	h.logger.WarnContext(request.Context(), "acquisition request failed", "method", request.Method, "path", request.URL.Path, "error", err)
	switch {
	case errors.Is(err, acquisitionservice.ErrInvalidSettings):
		writeError(writer, http.StatusUnprocessableEntity, "invalid_settings", "Enter a valid Prowlarr URL and API key.")
	case errors.Is(err, acquisitionservice.ErrSearchUnauthorized):
		writeError(writer, http.StatusUnauthorized, "search_unauthorized", "Prowlarr rejected that API key. Copy the API key from Prowlarr Settings and try again.")
	case errors.Is(err, domain.ErrNotConfigured):
		writeError(writer, http.StatusConflict, "search_not_configured", "Connect Prowlarr before searching for new media.")
	case errors.Is(err, acquisitionservice.ErrNotCached):
		writeError(writer, http.StatusNotFound, "not_cached", "No instant TorBox result is available for that title yet. Try another title or try again later.")
	case errors.Is(err, acquisitionservice.ErrNoPlayableMedia):
		writeError(writer, http.StatusUnprocessableEntity, "no_playable_media", "The instant result did not contain a matching MP4 or MKV video.")
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "provider_unauthorized", "Prowlarr or TorBox rejected a saved API key. Reconnect the provider and try again.")
	default:
		writeError(writer, http.StatusServiceUnavailable, "acquisition_unavailable", "BlackPearl could not add that title right now. Try again shortly.")
	}
}
