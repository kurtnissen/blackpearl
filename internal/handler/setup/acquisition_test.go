package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	setuphandler "github.com/blackpearl-media/blackpearl/internal/handler/setup"
	acquisitionservice "github.com/blackpearl-media/blackpearl/internal/service/acquisition"
	setupservice "github.com/blackpearl-media/blackpearl/internal/service/setup"
	watchlistservice "github.com/blackpearl-media/blackpearl/internal/service/watchlist"
	"github.com/stretchr/testify/require"
)

func TestHandlerWatchlistStatusRequiresPairingAndReturnsOnlyAggregates(t *testing.T) {
	t.Parallel()
	lastSync := time.Date(2026, time.August, 14, 14, 0, 0, 0, time.UTC)
	setup := &fakeService{}
	watchlist := &fakeWatchlistService{status: watchlistservice.ObserverStatus{
		Enabled: true, Healthy: true, AcquisitionEnabled: false, LastSyncAt: &lastSync,
		Queue: acquisitiondomain.WatchlistQueueStatus{PendingMovies: 2, Succeeded: 1, ObservedShows: 3},
	}}
	handler, err := setuphandler.NewWithAcquisitionAndWatchlist(setup, &fakeAcquisitionService{}, watchlist)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodGet, "/api/watchlist/status", csrf, "")
	request.Header.Del("Origin") // Same-origin browser GET requests omit Origin.
	request.Header.Set("X-BlackPearl-Session", "paired-session")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"enabled":true,"healthy":true,"acquisitionEnabled":false,"lastSyncAt":"2026-08-14T14:00:00Z","queue":{"pendingMovies":2,"acquiring":0,"succeeded":1,"notCached":0,"retryable":0,"manualReview":0,"observedShows":3}}`, response.Body.String())
	require.Equal(t, "paired-session", setup.authorizeSession)
	require.NotContains(t, response.Body.String(), "title")
	require.NotContains(t, response.Body.String(), "externalId")
}

func TestHandlerWatchlistStatusRejectsForeignOriginWhenPresent(t *testing.T) {
	t.Parallel()
	watchlist := &fakeWatchlistService{}
	handler, err := setuphandler.NewWithAcquisitionAndWatchlist(&fakeService{}, &fakeAcquisitionService{}, watchlist)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodGet, "/api/watchlist/status", csrf, "")
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("X-BlackPearl-Session", "paired-session")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.Zero(t, watchlist.statusCalls)
}

func TestHandlerWatchlistStatusRejectsUnpairedBrowserBeforeReadingQueue(t *testing.T) {
	t.Parallel()
	setup := &fakeService{authorizeErr: setupservice.ErrSetupUnauthorized}
	watchlist := &fakeWatchlistService{}
	handler, err := setuphandler.NewWithAcquisitionAndWatchlist(setup, &fakeAcquisitionService{}, watchlist)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodGet, "/api/watchlist/status", csrf, "")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Zero(t, watchlist.statusCalls)
}

func TestHandlerAcquisitionStatusReturnsNoPrivateSettings(t *testing.T) {
	t.Parallel()
	setup := &fakeService{}
	acquisition := &fakeAcquisitionService{status: acquisitionservice.CoordinatorStatus{Configured: true}}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8082/api/acquisition/status", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"configured":true}`, response.Body.String())
	require.NotContains(t, response.Body.String(), "prowlarr")
	require.NotContains(t, response.Body.String(), "apiKey")
}

func TestHandlerConfigureAcquisitionUsesSharedPairingAndNeverEchoesKey(t *testing.T) {
	t.Parallel()
	setup := &fakeService{}
	acquisition := &fakeAcquisitionService{}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPut, "/api/acquisition/settings", csrf, `{"baseUrl":"http://prowlarr:9696","apiKey":"private-prowlarr-key"}`)
	request.Header.Set("X-BlackPearl-Session", "paired-session")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "http://prowlarr:9696", acquisition.endpoint)
	require.Equal(t, "private-prowlarr-key", acquisition.credential)
	require.Equal(t, "paired-session", setup.authorizeSession)
	require.NotContains(t, response.Body.String(), "private-prowlarr-key")
	require.NotContains(t, response.Body.String(), "prowlarr:9696")
	require.Len(t, response.Header().Get("X-BlackPearl-Session"), 64)
}

func TestHandlerConfigureAcquisitionRejectsUnknownFieldsBeforeService(t *testing.T) {
	t.Parallel()
	setup := &fakeService{}
	acquisition := &fakeAcquisitionService{}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPut, "/api/acquisition/settings", csrf, `{"baseUrl":"http://prowlarr:9696","apiKey":"key","extra":true}`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, acquisition.configureCalls)
}

func TestHandlerAcquirePublishesMovieAndReturnsUpdatedPublicManifest(t *testing.T) {
	t.Parallel()
	media := acquiredForHandler(t, domain.MediaTypeMovie)
	configuration, err := domain.NewSetupConfiguration(media.Candidate(), "Example Movie", 2026)
	require.NoError(t, err)
	setup := &fakeService{status: setupservice.Status{SelectedItems: []domain.SetupConfiguration{configuration}}}
	acquisition := &fakeAcquisitionService{media: media}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPost, "/api/acquisition/acquire", csrf, `{"mediaType":"movie","title":"Example Movie","year":2026}`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, domain.MediaTypeMovie, acquisition.request.MediaType())
	require.Equal(t, "Example Movie", acquisition.request.Title())
	var body struct {
		Selected      domain.SetupConfiguration   `json:"selected"`
		SelectedItems []domain.SetupConfiguration `json:"selectedItems"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, configuration, body.Selected)
	require.Equal(t, []domain.SetupConfiguration{configuration}, body.SelectedItems)
}

func TestHandlerAcquireBuildsEpisodeIntent(t *testing.T) {
	t.Parallel()
	media := acquiredForHandler(t, domain.MediaTypeEpisode)
	configuration, err := domain.NewSetupEpisodeConfiguration(media.Candidate(), "Example Show", 2026, 7, 2, "Episode 2")
	require.NoError(t, err)
	setup := &fakeService{status: setupservice.Status{SelectedItems: []domain.SetupConfiguration{configuration}}}
	acquisition := &fakeAcquisitionService{media: media}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPost, "/api/acquisition/acquire", csrf, `{"mediaType":"episode","title":"Example Show","year":2026,"season":7,"episode":2}`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, domain.MediaTypeEpisode, acquisition.request.MediaType())
	require.Equal(t, 7, acquisition.request.Season())
	require.Equal(t, 2, acquisition.request.Episode())
}

func TestHandlerAcquireRejectsInvalidIntentBeforeMutation(t *testing.T) {
	t.Parallel()
	setup := &fakeService{}
	acquisition := &fakeAcquisitionService{}
	handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)
	request := newMutation(t, http.MethodPost, "/api/acquisition/acquire", csrf, `{"mediaType":"episode","title":"Example Show","year":2026,"season":7}`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
	require.Zero(t, acquisition.acquireCalls)
}

func TestHandlerSubmitsAndReadsPairedBackgroundAcquisitionJobs(t *testing.T) {
	t.Parallel()
	setup := &fakeService{}
	jobs := &fakeAcquisitionJobService{job: acquisitionJobForHandler(t, acquisitiondomain.JobStateQueued)}
	handler, err := setuphandler.NewWithAcquisitionAndJobs(setup, &fakeAcquisitionService{}, jobs)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)

	submit := newMutation(t, http.MethodPost, "/api/acquisition/jobs", csrf, `{"mediaType":"movie","title":"Example Movie","year":2026}`)
	submit.Header.Set("X-BlackPearl-Session", "paired-session")
	submitResponse := httptest.NewRecorder()
	handler.ServeHTTP(submitResponse, submit)

	require.Equal(t, http.StatusAccepted, submitResponse.Code)
	require.Equal(t, "Example Movie", jobs.request.Title())
	require.Contains(t, submitResponse.Body.String(), `"state":"queued"`)
	require.NotContains(t, submitResponse.Body.String(), "infoHash")
	require.NotContains(t, submitResponse.Body.String(), "provider")

	list := newMutation(t, http.MethodGet, "/api/acquisition/jobs", csrf, "")
	list.Header.Del("Origin")
	list.Header.Set("X-BlackPearl-Session", "paired-session")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	require.Equal(t, http.StatusOK, listResponse.Code)
	require.Contains(t, listResponse.Body.String(), jobs.job.ID())

	get := newMutation(t, http.MethodGet, "/api/acquisition/jobs/"+jobs.job.ID(), csrf, "")
	get.Header.Del("Origin")
	get.Header.Set("X-BlackPearl-Session", "paired-session")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	require.Equal(t, http.StatusOK, getResponse.Code)
	require.Equal(t, jobs.job.ID(), jobs.gotID)
}

func TestHandlerBackgroundJobsRequirePairingAndMapMissingJob(t *testing.T) {
	t.Parallel()
	setup := &fakeService{authorizeErr: setupservice.ErrSetupUnauthorized}
	jobs := &fakeAcquisitionJobService{getErr: domain.ErrNotFound}
	handler, err := setuphandler.NewWithAcquisitionAndJobs(setup, &fakeAcquisitionService{}, jobs)
	require.NoError(t, err)
	csrf := fetchCSRF(t, handler)

	unpaired := newMutation(t, http.MethodPost, "/api/acquisition/jobs", csrf, `{"mediaType":"movie","title":"Example","year":2026}`)
	unpairedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unpairedResponse, unpaired)
	require.Equal(t, http.StatusUnauthorized, unpairedResponse.Code)
	require.Zero(t, jobs.submitCalls)

	setup.authorizeErr = nil
	missing := newMutation(t, http.MethodGet, "/api/acquisition/jobs/0123456789abcdef0123456789abcdef", csrf, "")
	missing.Header.Del("Origin")
	missing.Header.Set("X-BlackPearl-Session", "paired-session")
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	require.Equal(t, http.StatusNotFound, missingResponse.Code)
	require.Contains(t, missingResponse.Body.String(), "job_not_found")
}

func TestHandlerMapsAcquisitionErrorsToPublicRecoveryMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		configure bool
	}{
		{name: "invalid settings", err: acquisitionservice.ErrInvalidSettings, status: http.StatusUnprocessableEntity, code: "invalid_settings", configure: true},
		{name: "search unauthorized", err: acquisitionservice.ErrSearchUnauthorized, status: http.StatusUnauthorized, code: "search_unauthorized", configure: true},
		{name: "not configured", err: domain.ErrNotConfigured, status: http.StatusConflict, code: "search_not_configured"},
		{name: "not cached", err: acquisitionservice.ErrNotCached, status: http.StatusNotFound, code: "not_cached"},
		{name: "no playable media", err: acquisitionservice.ErrNoPlayableMedia, status: http.StatusUnprocessableEntity, code: "no_playable_media"},
		{name: "provider unauthorized", err: domain.ErrUnauthorized, status: http.StatusUnauthorized, code: "provider_unauthorized"},
		{name: "unavailable", err: errors.New("private locator failure"), status: http.StatusServiceUnavailable, code: "acquisition_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			setup := &fakeService{}
			acquisition := &fakeAcquisitionService{}
			if test.configure {
				acquisition.configureErr = test.err
			} else {
				acquisition.acquireErr = test.err
			}
			handler, err := setuphandler.NewWithAcquisition(setup, acquisition)
			require.NoError(t, err)
			csrf := fetchCSRF(t, handler)
			path := "/api/acquisition/acquire"
			method := http.MethodPost
			body := `{"mediaType":"movie","title":"Example","year":2026}`
			if test.configure {
				path = "/api/acquisition/settings"
				method = http.MethodPut
				body = `{"baseUrl":"http://prowlarr:9696","apiKey":"key"}`
			}
			request := newMutation(t, method, path, csrf, body)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, test.status, response.Code)
			require.Contains(t, response.Body.String(), test.code)
			require.NotContains(t, response.Body.String(), "private locator")
		})
	}
}

type fakeAcquisitionService struct {
	status         acquisitionservice.CoordinatorStatus
	statusErr      error
	endpoint       string
	credential     string
	configureCalls int
	configureErr   error
	request        acquisitiondomain.SearchRequest
	acquireCalls   int
	media          acquisitiondomain.AcquiredMedia
	acquireErr     error
}

type fakeAcquisitionJobService struct {
	job         acquisitiondomain.AcquisitionJob
	request     acquisitiondomain.SearchRequest
	submitCalls int
	submitErr   error
	getErr      error
	listErr     error
	gotID       string
}

func (f *fakeAcquisitionJobService) Submit(_ context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquisitionJob, bool, error) {
	f.submitCalls++
	f.request = request
	return f.job, true, f.submitErr
}

func (f *fakeAcquisitionJobService) Get(_ context.Context, id string) (acquisitiondomain.AcquisitionJob, error) {
	f.gotID = id
	return f.job, f.getErr
}

func (f *fakeAcquisitionJobService) List(context.Context, int) ([]acquisitiondomain.AcquisitionJob, error) {
	return []acquisitiondomain.AcquisitionJob{f.job}, f.listErr
}

func acquisitionJobForHandler(t *testing.T, state acquisitiondomain.JobState) acquisitiondomain.AcquisitionJob {
	t.Helper()
	request, err := acquisitiondomain.NewMovieSearch("Example Movie", 2026)
	require.NoError(t, err)
	at := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job, err := acquisitiondomain.NewAcquisitionJobSnapshot(acquisitiondomain.JobSnapshotInput{
		ID: "0123456789abcdef0123456789abcdef", Request: request, State: state,
		CreatedAt: at, UpdatedAt: at,
	})
	require.NoError(t, err)
	return job
}

type fakeWatchlistService struct {
	status      watchlistservice.ObserverStatus
	statusErr   error
	statusCalls int
}

func (f *fakeWatchlistService) Status(context.Context) (watchlistservice.ObserverStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func (f *fakeAcquisitionService) Status(context.Context) (acquisitionservice.CoordinatorStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeAcquisitionService) Configure(_ context.Context, endpoint string, credential string) error {
	f.configureCalls++
	f.endpoint = endpoint
	f.credential = credential
	return f.configureErr
}

func (f *fakeAcquisitionService) Acquire(_ context.Context, request acquisitiondomain.SearchRequest) (acquisitiondomain.AcquiredMedia, error) {
	f.acquireCalls++
	f.request = request
	return f.media, f.acquireErr
}

func acquiredForHandler(t *testing.T, mediaType domain.MediaType) acquisitiondomain.AcquiredMedia {
	t.Helper()
	var request acquisitiondomain.SearchRequest
	var err error
	name := "Example.Movie.2026.mkv"
	if mediaType == domain.MediaTypeEpisode {
		request, err = acquisitiondomain.NewEpisodeSearch("Example Show", 2026, 7, 2)
		name = "Example.Show.S07E02.mkv"
	} else {
		request, err = acquisitiondomain.NewMovieSearch("Example Movie", 2026)
	}
	require.NoError(t, err)
	release, err := acquisitiondomain.NewRelease(acquisitiondomain.ReleaseInput{
		Provider: "prowlarr", SourceID: "release", Title: request.Query(), Protocol: acquisitiondomain.ReleaseProtocolTorrent,
		Size: 20, Indexer: "authorized", InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	candidate, err := domain.NewMediaCandidate("18:2", name, 20)
	require.NoError(t, err)
	media, err := acquisitiondomain.NewAcquiredMedia(request, release, candidate)
	require.NoError(t, err)
	return media
}
