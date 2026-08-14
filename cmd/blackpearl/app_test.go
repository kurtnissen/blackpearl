package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/config"
	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/gateway/internetarchive"
	"github.com/kurtnissen/blackpearl/internal/pearlfs"
	acquisitionrepo "github.com/kurtnissen/blackpearl/internal/repository/acquisition"
	acquisitionjobrepo "github.com/kurtnissen/blackpearl/internal/repository/acquisitionjob"
	setuprepo "github.com/kurtnissen/blackpearl/internal/repository/setup"
	watchlistrepo "github.com/kurtnissen/blackpearl/internal/repository/watchlist"
	setupservice "github.com/kurtnissen/blackpearl/internal/service/setup"
	"github.com/kurtnissen/blackpearl/internal/state"
	"github.com/stretchr/testify/require"
)

func TestRunImportsPOCAndUnmountsOnCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	mounted := make(chan struct{})
	server := &fakeMountServer{}
	deps := dependencies{
		mount: func(_ context.Context, _ string, filesystem *pearlfs.Root) (mountServer, error) {
			require.NotNil(t, filesystem)
			close(mounted)
			return server, nil
		},
		listen: net.Listen,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)

	go func() {
		result <- run(ctx, cfg, testLogger(), deps)
	}()
	select {
	case <-mounted:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not reach mount")
	}
	cancel()
	require.NoError(t, <-result)
	require.True(t, server.unmounted)

	repository, err := state.Open(context.Background(), cfg.DBPath)
	require.NoError(t, err)
	media, err := repository.List(context.Background())
	require.NoError(t, err)
	require.Len(t, media, 1)
	require.Equal(t, domain.MediaID("blackpearl-poc-2026"), media[0].ID)
	require.NoError(t, repository.Close())
}

func TestRunCleansDatabaseWhenMountFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	deps := dependencies{
		mount: func(context.Context, string, *pearlfs.Root) (mountServer, error) {
			return nil, errors.New("fuse unavailable")
		},
		listen: net.Listen,
	}

	err := run(context.Background(), cfg, testLogger(), deps)

	require.ErrorContains(t, err, "mount PearlFS")
	repository, reopenErr := state.Open(context.Background(), cfg.DBPath)
	require.NoError(t, reopenErr)
	require.NoError(t, repository.Close())
}

func TestSetupPublisherNotifiesOnlyAfterSuccessfulAtomicPublication(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		notifier := &fakePublicationNotifier{}
		switcher := core.NewCatalogSwitch()
		publisher := &setupPublisher{
			switcher: switcher,
			nfs:      &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")},
			notifier: notifier,
		}
		next := &fakeReadyCatalog{}

		err := publisher.Publish(context.Background(), next)

		require.NoError(t, err)
		require.Equal(t, int32(1), notifier.calls.Load())
		require.NoError(t, switcher.Ready(context.Background()))
	})
	t.Run("NFS replacement failure", func(t *testing.T) {
		t.Parallel()
		notifier := &fakePublicationNotifier{}
		switcher := core.NewCatalogSwitch()
		publisher := &setupPublisher{
			switcher: switcher,
			nfs: &fakeNFSServer{
				address: fakeAddress("127.0.0.1:2049"), replaceErr: errors.New("NFS unavailable"),
			},
			notifier: notifier,
		}

		err := publisher.Publish(context.Background(), &fakeReadyCatalog{})

		require.ErrorContains(t, err, "NFS unavailable")
		require.Zero(t, notifier.calls.Load())
		require.ErrorIs(t, switcher.Ready(context.Background()), domain.ErrNotConfigured)
	})
}

func testConfig(root string, source string) config.Config {
	return config.Config{
		DataDir:                     filepath.Join(root, "data"),
		DBPath:                      filepath.Join(root, "data", "blackpearl.db"),
		CacheDir:                    filepath.Join(root, "data", "cache"),
		MountPath:                   filepath.Join(root, "mount"),
		POCSource:                   source,
		HTTPAddr:                    "127.0.0.1:0",
		LogLevel:                    "debug",
		StorageMode:                 domain.StorageModePersistent,
		CacheChunkBytes:             262_144,
		RangeTimeout:                30 * time.Second,
		AcquisitionOperationTimeout: 2 * time.Minute,
		FilesystemMode:              "fuse",
		NFSAddr:                     "127.0.0.1:0",
	}
}

func TestRunStartsNFSWithoutInvokingFUSEAndStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	cfg.FilesystemMode = "nfs"
	started := make(chan struct{})
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	var fuseCalled atomic.Bool
	deps := dependencies{
		mount: func(context.Context, string, *pearlfs.Root) (mountServer, error) {
			fuseCalled.Store(true)
			return nil, errors.New("FUSE must not start in NFS mode")
		},
		serveNFS: func(_ context.Context, address string, catalog nfsCatalog) (nfsServer, error) {
			require.Equal(t, cfg.NFSAddr, address)
			require.NotNil(t, catalog)
			close(started)
			return nfs, nil
		},
		listen: net.Listen,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, cfg, testLogger(), deps)
	}()
	select {
	case <-started:
	case err := <-result:
		require.NoError(t, err)
		return
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start NFS")
	}
	cancel()

	require.NoError(t, <-result)
	require.False(t, fuseCalled.Load())
	require.True(t, nfs.closed)
	require.True(t, nfs.waited)
	_, err := os.Stat(cfg.MountPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunCleansDatabaseWhenNFSStartFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "fixture.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic-video"), 0o600))
	cfg := testConfig(root, source)
	cfg.FilesystemMode = "nfs"
	deps := defaultDependencies()
	deps.serveNFS = func(context.Context, string, nfsCatalog) (nfsServer, error) {
		return nil, errors.New("NFS unavailable")
	}

	err := run(context.Background(), cfg, testLogger(), deps)

	require.ErrorContains(t, err, "start PearlNFS")
	repository, reopenErr := state.Open(context.Background(), cfg.DBPath)
	require.NoError(t, reopenErr)
	require.NoError(t, repository.Close())
}

func TestRunRollingModeRegistersRemotePOCAndStartsNFS(t *testing.T) {
	root := t.TempDir()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/media/movie.mp4", request.URL.Path)
		require.Equal(t, http.MethodHead, request.Method)
		writer.Header().Set("Content-Length", "8")
		writer.Header().Set("ETag", `"rolling-v1"`)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(origin.Close)
	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 8
	cfg.CacheChunkBytes = 4
	cfg.RangeOriginURL = origin.URL + "/media/"
	cfg.RangeObjectID = "movie.mp4"
	cfg.RangeTimeout = time.Second
	cfg.FilesystemMode = "nfs"
	started := make(chan struct{})
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	deps := defaultDependencies()
	deps.httpClient = origin.Client()
	deps.serveNFS = func(_ context.Context, _ string, catalog nfsCatalog) (nfsServer, error) {
		items, err := catalog.List(context.Background())
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, int64(8), items[0].Size)
		require.Equal(t, domain.BackingRef{Provider: "http-range", ObjectID: "movie.mp4"}, items[0].Backing)
		close(started)
		return nfs, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, cfg, testLogger(), deps)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("rolling service did not start NFS")
	}
	cancel()

	require.NoError(t, <-result)
	require.True(t, nfs.closed)
	require.True(t, nfs.waited)
	require.NoError(t, filepath.WalkDir(cfg.CacheDir, func(path string, entry os.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		require.NotEqual(t, ".mp4", filepath.Ext(entry.Name()), path)
		return nil
	}))
}

func TestRunRollingTorBoxRegistersRemotePOCAndStartsNFS(t *testing.T) {
	root := t.TempDir()
	var api *httptest.Server
	api = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			require.Equal(t, "Bearer secret-token", request.Header.Get("Authorization"))
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"size":16,"hash":"fixture-hash","zipped":false,"infected":false}]}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			require.Equal(t, "Bearer secret-token", request.Header.Get("Authorization"))
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, api.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			require.Empty(t, request.Header.Get("Authorization"))
			require.Equal(t, http.MethodGet, request.Method)
			require.Equal(t, "bytes=0-0", request.Header.Get("Range"))
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write([]byte("0"))
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(api.Close)
	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 8
	cfg.CacheChunkBytes = 4
	cfg.RangeProvider = "torbox-torrent"
	cfg.RangeObjectID = "17:3"
	cfg.RangeTimeout = time.Second
	cfg.TorBoxAPIURL = api.URL + "/v1/api/"
	cfg.TorBoxAPIToken = "secret-token"
	cfg.FilesystemMode = "nfs"
	started := make(chan struct{})
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	deps := defaultDependencies()
	deps.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("instrumented shared client must not receive TorBox secrets")
	})}
	deps.torBoxClient = api.Client()
	deps.serveNFS = func(_ context.Context, _ string, catalog nfsCatalog) (nfsServer, error) {
		items, err := catalog.List(context.Background())
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, int64(16), items[0].Size)
		require.Equal(t, domain.BackingRef{Provider: "torbox-torrent", ObjectID: "17:3"}, items[0].Backing)
		close(started)
		return nfs, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("TorBox rolling service did not start NFS")
	}
	cancel()

	require.NoError(t, <-result)
	require.True(t, nfs.closed)
	require.True(t, nfs.waited)
}

func TestRunBrowserSetupStartsWithoutCredentialsAndServesSetupStatus(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = "https://api.example.invalid/v1/api/"
	nfsStarted := make(chan struct{})
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.serveNFS = func(_ context.Context, _ string, catalog nfsCatalog) (nfsServer, error) {
		items, err := catalog.List(context.Background())
		require.NoError(t, err)
		require.Empty(t, items)
		close(nfsStarted)
		return nfs, nil
	}
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()

	select {
	case <-nfsStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("setup NFS did not start")
	}
	address := <-httpAddress
	response, err := http.Get("http://" + address + "/api/setup/status")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), `"setupRequired":true`)
	require.NotContains(t, string(body), "tokenFilename")
	cancel()
	require.NoError(t, <-result)
}

func TestRunBrowserSetupObservesPlexWatchlistWithoutAcquiring(t *testing.T) {
	root := t.TempDir()
	credentialPath := filepath.Join(root, "plex-token")
	require.NoError(t, os.WriteFile(credentialPath, []byte("private-plex-token"), 0o600))
	requested := make(chan struct{}, 1)
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/library/sections/watchlist/all":
			require.Equal(t, "private-plex-token", request.Header.Get("X-Plex-Token"))
			select {
			case requested <- struct{}{}:
			default:
			}
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":2,"totalSize":2,"Metadata":[{"guid":"plex://movie/one","type":"movie","title":"Private Movie","year":2026},{"guid":"plex://show/one","type":"show","title":"Private Show","year":2025}]}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/mylist":
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"name":"Existing.Movie.2025.mkv","size":16,"hash":"existing-file-hash","zipped":false,"infected":false}]}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, provider.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write([]byte("0"))
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = provider.URL + "/v1/api/"
	cfg.WatchlistEnabled = true
	cfg.WatchlistBaseURL = provider.URL
	cfg.WatchlistPollInterval = time.Hour
	cfg.WatchlistTokenFile = credentialPath
	cfg.WatchlistAcquisitionTimeout = 30 * time.Second
	cfg.WatchlistLeaseDuration = 2 * time.Minute
	cfg.WatchlistWorkerIdleInterval = time.Second
	cfg.WatchlistReconcileInterval = 5 * time.Second
	cfg.WatchlistNotCachedCooldown = time.Minute
	cfg.WatchlistRetryCooldown = time.Minute
	setupRepository, err := setuprepo.New(cfg.SetupDir)
	require.NoError(t, err)
	existingCandidate, err := domain.NewMediaCandidate("17:3", "Existing.Movie.2025.mkv", 16)
	require.NoError(t, err)
	existing, err := domain.NewSetupConfiguration(existingCandidate, "Existing Movie", 2025)
	require.NoError(t, err)
	existingManifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{existing})
	require.NoError(t, err)
	require.NoError(t, setupRepository.SaveManifest(context.Background(), "saved-torbox-token", existingManifest))
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.httpClient = provider.Client()
	deps.torBoxClient = provider.Client()
	deps.serveNFS = func(context.Context, string, nfsCatalog) (nfsServer, error) { return nfs, nil }
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	var address string
	select {
	case address = <-httpAddress:
	case runErr := <-result:
		require.NoError(t, runErr)
		require.FailNow(t, "browser setup stopped before HTTP startup")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "browser setup did not start HTTP")
	}
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("watchlist was not observed")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	setupResponse, err := client.Get("http://" + address + "/api/setup/status")
	require.NoError(t, err)
	var setupStatus struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.NewDecoder(setupResponse.Body).Decode(&setupStatus))
	require.NoError(t, setupResponse.Body.Close())

	var watchlistStatus struct {
		Enabled bool                                   `json:"enabled"`
		Healthy bool                                   `json:"healthy"`
		Queue   acquisitiondomain.WatchlistQueueStatus `json:"queue"`
	}
	require.Eventually(t, func() bool {
		request, requestErr := http.NewRequest(http.MethodGet, "http://"+address+"/api/watchlist/status", nil)
		if requestErr != nil {
			return false
		}
		request.Header.Set("Origin", "http://"+address)
		request.Header.Set("X-BlackPearl-CSRF", setupStatus.CSRFToken)
		request.Header.Set("X-BlackPearl-Bootstrap", cfg.SetupBootstrapToken)
		response, responseErr := client.Do(request)
		if responseErr != nil {
			return false
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			return false
		}
		if bytes.Contains(body, []byte("Private Movie")) || bytes.Contains(body, []byte("private-plex-token")) {
			return false
		}
		return json.Unmarshal(body, &watchlistStatus) == nil && watchlistStatus.Healthy &&
			watchlistStatus.Queue.PendingMovies == 1 && watchlistStatus.Queue.ObservedShows == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.True(t, watchlistStatus.Enabled)
	require.Equal(t, 1, watchlistStatus.Queue.PendingMovies)
	require.Equal(t, 1, watchlistStatus.Queue.ObservedShows)
	require.Zero(t, watchlistStatus.Queue.Acquiring)

	cancel()
	require.NoError(t, <-result)
}

func TestRunBrowserSetupAdvancesPublishedEpisodeFromPlexPlayback(t *testing.T) {
	root := t.TempDir()
	credentialPath := filepath.Join(root, "plex-token")
	require.NoError(t, os.WriteFile(credentialPath, []byte("private-plex-token"), 0o600))
	showID := "plex://show/5d9c086ce98e47001eb0f520"
	seasonID := "5d9c09de2192ba001f32230f"
	var provider *httptest.Server

	candidate, err := domain.NewMediaCandidate("17:3", "MariposaHD.S01E01.mp4", 16)
	require.NoError(t, err)
	episode, err := domain.NewSetupEpisodeConfiguration(candidate, "MariposaHD", 2006, 1, 1, "Episode 1")
	require.NoError(t, err)
	virtualPath, err := episode.VirtualPath()
	require.NoError(t, err)
	provider = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/library/") || request.URL.Path == "/status/sessions" {
			require.Equal(t, "private-plex-token", request.Header.Get("X-Plex-Token"))
		}
		switch request.URL.Path {
		case "/library/sections/watchlist/all":
			_, writeErr := writer.Write([]byte(`{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[{"guid":"plex://show/5d9c086ce98e47001eb0f520","type":"show","title":"MariposaHD","year":2006}]}}`))
			require.NoError(t, writeErr)
		case "/status/sessions":
			_, writeErr := fmt.Fprintf(writer, `{"MediaContainer":{"size":1,"Metadata":[{"type":"episode","grandparentGuid":%q,"parentIndex":1,"index":1,"viewOffset":300000,"duration":1200000,"Player":{"state":"playing"},"Media":[{"Part":[{"file":%q,"selected":true}]}]}]}}`, showID, "/blackpearl/"+virtualPath)
			require.NoError(t, writeErr)
		case "/library/metadata/5d9c086ce98e47001eb0f520/children":
			_, writeErr := fmt.Fprintf(writer, `{"MediaContainer":{"size":1,"Metadata":[{"type":"season","index":1,"ratingKey":%q}]}}`, seasonID)
			require.NoError(t, writeErr)
		case "/library/metadata/" + seasonID + "/children":
			_, writeErr := writer.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[{"type":"episode","parentIndex":1,"index":1},{"type":"episode","parentIndex":1,"index":2}]}}`))
			require.NoError(t, writeErr)
		case "/library/sections":
			_, writeErr := writer.Write([]byte(`{"MediaContainer":{"size":2,"Directory":[{"key":"2","type":"movie","Location":[{"id":2,"path":"/blackpearl/Movies"}]},{"key":"3","type":"show","Location":[{"id":3,"path":"/blackpearl/TV Shows"}]}]}}`))
			require.NoError(t, writeErr)
		case "/library/sections/2/refresh", "/library/sections/3/refresh":
			writer.WriteHeader(http.StatusOK)
		case "/v1/api/torrents/mylist":
			_, writeErr := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"name":"MariposaHD.S01E01.mp4","size":16,"hash":"episode-file-hash","zipped":false,"infected":false}]}}`))
			require.NoError(t, writeErr)
		case "/v1/api/torrents/requestdl":
			_, writeErr := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, provider.URL+"/cdn/file")
			require.NoError(t, writeErr)
		case "/cdn/file":
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, writeErr := writer.Write([]byte("0"))
			require.NoError(t, writeErr)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = provider.URL + "/v1/api/"
	cfg.RangeTimeout = time.Second
	cfg.WatchlistEnabled = true
	cfg.WatchlistBaseURL = provider.URL
	cfg.WatchlistPollInterval = time.Hour
	cfg.WatchlistTokenFile = credentialPath
	cfg.WatchlistAcquisitionEnabled = true
	cfg.WatchlistLeaseDuration = time.Minute
	cfg.WatchlistAcquisitionTimeout = 30 * time.Second
	cfg.WatchlistWorkerIdleInterval = 5 * time.Millisecond
	cfg.WatchlistReconcileInterval = 5 * time.Millisecond
	cfg.WatchlistNotCachedCooldown = time.Hour
	cfg.WatchlistRetryCooldown = time.Minute
	cfg.PlexRefreshEnabled = true
	cfg.PlexRefreshURL = provider.URL
	cfg.PlaybackAdvancementEnabled = true
	cfg.PlaybackPollInterval = 5 * time.Millisecond
	cfg.PlaybackOperationTimeout = time.Second
	cfg.PlaybackMetadataURL = provider.URL

	setupRepository, err := setuprepo.New(cfg.SetupDir)
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{episode})
	require.NoError(t, err)
	require.NoError(t, setupRepository.SaveManifest(context.Background(), "saved-torbox-token", manifest))
	queue, err := watchlistrepo.Open(context.Background(), cfg.DBPath, true)
	require.NoError(t, err)
	policy, err := acquisitiondomain.NewWatchlistPolicy(true, acquisitiondomain.WatchlistShowPolicyPilot)
	require.NoError(t, err)
	require.NoError(t, queue.SetPolicy(context.Background(), policy))
	item, err := acquisitiondomain.NewWatchlistItem(acquisitiondomain.WatchlistItemInput{
		Source: "plex-watchlist", ExternalID: showID, MediaType: acquisitiondomain.WatchlistMediaTypeShow,
		Title: "MariposaHD", Year: 2006,
	})
	require.NoError(t, err)
	observation, err := acquisitiondomain.NewWatchlistObservation(item, true, 1, 1)
	require.NoError(t, err)
	observedAt := time.Now().UTC()
	require.NoError(t, queue.UpsertObservations(context.Background(), []acquisitiondomain.WatchlistObservation{observation}, observedAt))
	claim, err := queue.Claim(context.Background(), observedAt, time.Minute)
	require.NoError(t, err)
	completion, err := acquisitiondomain.NewWatchlistSucceeded(episode.Backing().ObjectID)
	require.NoError(t, err)
	require.NoError(t, queue.Complete(context.Background(), claim, completion, observedAt))
	require.NoError(t, queue.Close())

	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.httpClient = provider.Client()
	deps.torBoxClient = provider.Client()
	deps.serveNFS = func(context.Context, string, nfsCatalog) (nfsServer, error) { return nfs, nil }
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, listenErr := net.Listen(network, address)
		if listenErr == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, listenErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	select {
	case <-httpAddress:
	case runErr := <-result:
		require.NoError(t, runErr)
		require.FailNow(t, "browser setup stopped before HTTP startup")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "browser setup did not start HTTP")
	}

	jobQueue, err := acquisitionjobrepo.Open(context.Background(), filepath.Join(cfg.DataDir, "acquisition-jobs.db"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		jobs, listErr := jobQueue.List(context.Background(), 20)
		return listErr == nil && len(jobs) == 1 && jobs[0].Request().MediaType() == domain.MediaTypeEpisode &&
			jobs[0].Request().Season() == 1 && jobs[0].Request().Episode() == 2
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, jobQueue.Close())

	cancel()
	require.NoError(t, <-result)
}

func TestRunBrowserSetupSubmitsWatchlistMovieToDurableQueue(t *testing.T) {
	root := t.TempDir()
	credentialPath := filepath.Join(root, "plex-token")
	require.NoError(t, os.WriteFile(credentialPath, []byte("private-plex-token"), 0o600))
	var createCalls atomic.Int32
	var cacheCheckCalls atomic.Int32
	var watchlistCalls atomic.Int32
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/library/sections/watchlist/all":
			require.Equal(t, "private-plex-token", request.Header.Get("X-Plex-Token"))
			if watchlistCalls.Add(1) == 1 {
				_, err := writer.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":0,"Metadata":[]}}`))
				require.NoError(t, err)
				return
			}
			_, err := writer.Write([]byte(`{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[{"guid":"plex://movie/auto","type":"movie","title":"Automatic Movie","year":2026}]}}`))
			require.NoError(t, err)
		case "/prowlarr/api/v1/search":
			require.Equal(t, "private-prowlarr-key", request.Header.Get("X-Api-Key"))
			require.Equal(t, "Automatic Movie 2026", request.URL.Query().Get("query"))
			_, err := writer.Write([]byte(`[{"id":1,"guid":"release","size":16,"indexerId":1,"indexer":"Authorized","title":"Automatic.Movie.2026.1080p","protocol":"torrent","infoHash":"0123456789abcdef0123456789abcdef01234567","magnetUrl":"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567","seeders":20}]`))
			require.NoError(t, err)
		case "/v1/api/torrents/checkcached":
			cacheCheckCalls.Add(1)
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"0123456789abcdef0123456789abcdef01234567":{"name":"cached","size":16,"hash":"0123456789abcdef0123456789abcdef01234567"}}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/createtorrent":
			createCalls.Add(1)
			_, err := writer.Write([]byte(`{"success":true,"detail":"added","data":{"hash":"0123456789abcdef0123456789abcdef01234567","torrent_id":18,"auth_id":"redacted"}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/mylist":
			torrentID := request.URL.Query().Get("id")
			fileID := "3"
			name := "Existing.Movie.2025.mkv"
			if torrentID == "18" {
				fileID = "2"
				name = "Automatic.Movie.2026.mkv"
			}
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":{"id":%s,"download_finished":true,"download_present":true,"files":[{"id":%s,"name":%q,"size":16,"hash":"file-hash","zipped":false,"infected":false}]}}`, torrentID, fileID, name)
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, provider.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write([]byte("0"))
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = provider.URL + "/v1/api/"
	cfg.RangeTimeout = time.Second
	cfg.WatchlistEnabled = true
	cfg.WatchlistBaseURL = provider.URL
	cfg.WatchlistPollInterval = 5 * time.Millisecond
	cfg.WatchlistTokenFile = credentialPath
	cfg.WatchlistAcquisitionEnabled = true
	cfg.WatchlistLeaseDuration = time.Minute
	cfg.WatchlistAcquisitionTimeout = 30 * time.Second
	cfg.WatchlistWorkerIdleInterval = 5 * time.Millisecond
	cfg.WatchlistReconcileInterval = 5 * time.Millisecond
	cfg.WatchlistNotCachedCooldown = time.Hour
	cfg.WatchlistRetryCooldown = time.Minute
	setupRepository, err := setuprepo.New(cfg.SetupDir)
	require.NoError(t, err)
	existingCandidate, err := domain.NewMediaCandidate("17:3", "Existing.Movie.2025.mkv", 16)
	require.NoError(t, err)
	existing, err := domain.NewSetupConfiguration(existingCandidate, "Existing Movie", 2025)
	require.NoError(t, err)
	existingManifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{existing})
	require.NoError(t, err)
	require.NoError(t, setupRepository.SaveManifest(context.Background(), "saved-torbox-token", existingManifest))
	searchRepository, err := acquisitionrepo.New(filepath.Join(cfg.SetupDir, "acquisition"))
	require.NoError(t, err)
	searchSettings, err := acquisitiondomain.NewSearchProviderSettings("prowlarr", provider.URL+"/prowlarr/", "private-prowlarr-key")
	require.NoError(t, err)
	require.NoError(t, searchRepository.Save(context.Background(), searchSettings))

	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.httpClient = provider.Client()
	deps.torBoxClient = provider.Client()
	deps.serveNFS = func(context.Context, string, nfsCatalog) (nfsServer, error) { return nfs, nil }
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, listenErr := net.Listen(network, address)
		if listenErr == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, listenErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	<-httpAddress
	jobQueue, err := acquisitionjobrepo.Open(context.Background(), filepath.Join(cfg.DataDir, "acquisition-jobs.db"))
	require.NoError(t, err)
	var plannedJob acquisitiondomain.AcquisitionJob
	require.Eventually(t, func() bool {
		jobs, listErr := jobQueue.List(context.Background(), 20)
		if listErr != nil || len(jobs) != 1 || jobs[0].Request().Title() != "Automatic Movie" || jobs[0].State() != acquisitiondomain.JobStateSelected {
			return false
		}
		candidates, candidatesErr := jobQueue.Candidates(context.Background(), jobs[0].ID())
		if candidatesErr != nil || len(candidates) != 1 {
			return false
		}
		plannedJob = jobs[0]
		return candidates[0].Outcome() == acquisitiondomain.CandidateOutcomeSelected
	}, 5*time.Second, 10*time.Millisecond)
	selectedOrdinal, hasPlan := plannedJob.SelectedCandidateOrdinal()
	require.True(t, hasPlan)
	require.Zero(t, selectedOrdinal)
	require.NoError(t, jobQueue.Close())
	queue, err := watchlistrepo.Open(context.Background(), cfg.DBPath, cfg.WatchlistAcquisitionEnabled)
	require.NoError(t, err)
	var queueStatus acquisitiondomain.WatchlistQueueStatus
	require.Eventually(t, func() bool {
		var statusErr error
		queueStatus, statusErr = queue.Status(context.Background())
		return statusErr == nil && queueStatus.Acquiring+queueStatus.Succeeded == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, queue.Close())
	require.Equal(t, 1, queueStatus.Acquiring+queueStatus.Succeeded)
	require.Equal(t, int32(1), cacheCheckCalls.Load())

	cancel()
	require.NoError(t, <-result)
}

func TestRunBrowserSetupConfiguresSearchAndAcquiresCachedMovie(t *testing.T) {
	root := t.TempDir()
	content := []byte("0123456789abcdef")
	var torboxAPI *httptest.Server
	torboxAPI = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/checkcached":
			require.Equal(t, http.MethodPost, request.Method)
			require.Equal(t, "Bearer saved-torbox-token", request.Header.Get("Authorization"))
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"0123456789abcdef0123456789abcdef01234567":{"name":"cached","size":16,"hash":"0123456789abcdef0123456789abcdef01234567"}}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/createtorrent":
			require.NoError(t, request.ParseMultipartForm(1<<20))
			require.Equal(t, "true", request.FormValue("add_only_if_cached"))
			_, err := writer.Write([]byte(`{"success":true,"detail":"added","data":{"hash":"0123456789abcdef0123456789abcdef01234567","torrent_id":18,"auth_id":"redacted"}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/mylist":
			torrentID := request.URL.Query().Get("id")
			fileID := "3"
			name := "Existing.Movie.2025.mkv"
			fileHash := "existing-file-hash"
			if torrentID == "18" {
				fileID = "2"
				name = "Example.Movie.2026.mkv"
				fileHash = "acquired-file-hash"
			}
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":{"id":%s,"download_finished":true,"download_present":true,"files":[{"id":%s,"name":%q,"size":16,"hash":%q,"zipped":false,"infected":false}]}}`, torrentID, fileID, name, fileHash)
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, torboxAPI.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			require.Equal(t, "bytes=0-0", request.Header.Get("Range"))
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write(content[:1])
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(torboxAPI.Close)

	prowlarrAPI := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "private-prowlarr-key", request.Header.Get("X-Api-Key"))
		switch request.URL.Path {
		case "/base/api/v1/health":
			_, err := writer.Write([]byte(`[]`))
			require.NoError(t, err)
		case "/base/api/v1/search":
			require.Equal(t, "Example Movie 2026", request.URL.Query().Get("query"))
			_, err := writer.Write([]byte(`[{"id":1,"guid":"release","size":16,"indexerId":1,"indexer":"Authorized","title":"Example.Movie.2026.1080p","protocol":"torrent","infoHash":"0123456789abcdef0123456789abcdef01234567","seeders":20}]`))
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(prowlarrAPI.Close)

	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = torboxAPI.URL + "/v1/api/"
	cfg.RangeTimeout = time.Second
	setupRepository, err := setuprepo.New(cfg.SetupDir)
	require.NoError(t, err)
	existingCandidate, err := domain.NewMediaCandidate("17:3", "Existing.Movie.2025.mkv", 16)
	require.NoError(t, err)
	existing, err := domain.NewSetupConfiguration(existingCandidate, "Existing Movie", 2025)
	require.NoError(t, err)
	existingManifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{existing})
	require.NoError(t, err)
	require.NoError(t, setupRepository.SaveManifest(context.Background(), "saved-torbox-token", existingManifest))

	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.torBoxClient = torboxAPI.Client()
	deps.httpClient = prowlarrAPI.Client()
	deps.serveNFS = func(context.Context, string, nfsCatalog) (nfsServer, error) { return nfs, nil }
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, listenErr := net.Listen(network, address)
		if listenErr == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, listenErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	var address string
	select {
	case address = <-httpAddress:
	case runErr := <-result:
		require.NoError(t, runErr)
		require.FailNow(t, "browser setup stopped before HTTP startup")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "browser setup did not start HTTP")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	statusResponse, err := client.Get("http://" + address + "/api/setup/status")
	require.NoError(t, err)
	var status struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.NewDecoder(statusResponse.Body).Decode(&status))
	require.NoError(t, statusResponse.Body.Close())

	settingsRequest, err := http.NewRequest(http.MethodPut, "http://"+address+"/api/acquisition/settings", bytes.NewBufferString(fmt.Sprintf(`{"baseUrl":%q,"apiKey":"private-prowlarr-key"}`, prowlarrAPI.URL+"/base/")))
	require.NoError(t, err)
	settingsRequest.Header.Set("Content-Type", "application/json")
	settingsRequest.Header.Set("Origin", "http://"+address)
	settingsRequest.Header.Set("X-BlackPearl-CSRF", status.CSRFToken)
	settingsRequest.Header.Set("X-BlackPearl-Bootstrap", cfg.SetupBootstrapToken)
	settingsResponse, err := client.Do(settingsRequest)
	require.NoError(t, err)
	settingsBody, err := io.ReadAll(settingsResponse.Body)
	require.NoError(t, err)
	require.NoError(t, settingsResponse.Body.Close())
	require.Equal(t, http.StatusOK, settingsResponse.StatusCode, string(settingsBody))
	require.NotContains(t, string(settingsBody), "private-prowlarr-key")

	acquireRequest, err := http.NewRequest(http.MethodPost, "http://"+address+"/api/acquisition/acquire", bytes.NewBufferString(`{"mediaType":"movie","title":"Example Movie","year":2026}`))
	require.NoError(t, err)
	acquireRequest.Header.Set("Content-Type", "application/json")
	acquireRequest.Header.Set("Origin", "http://"+address)
	acquireRequest.Header.Set("X-BlackPearl-CSRF", status.CSRFToken)
	acquireRequest.Header.Set("X-BlackPearl-Bootstrap", cfg.SetupBootstrapToken)
	acquireResponse, err := client.Do(acquireRequest)
	require.NoError(t, err)
	acquireBody, err := io.ReadAll(acquireResponse.Body)
	require.NoError(t, err)
	require.NoError(t, acquireResponse.Body.Close())
	require.Equal(t, http.StatusOK, acquireResponse.StatusCode, string(acquireBody))
	var acquired struct {
		SelectedItems []domain.SetupConfiguration `json:"selectedItems"`
	}
	require.NoError(t, json.Unmarshal(acquireBody, &acquired))
	require.Len(t, acquired.SelectedItems, 2)
	require.Equal(t, "18:2", acquired.SelectedItems[1].ObjectID)

	cancel()
	require.NoError(t, <-result)
	_, persisted, err := setupRepository.LoadManifest(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted.Items, 2)
}

func TestRunBrowserSetupSelectedMediaUsesConfiguredRangeRetention(t *testing.T) {
	for _, storageMode := range []domain.StorageMode{domain.StorageModeRolling, domain.StorageModePersistent} {
		t.Run(string(storageMode), func(t *testing.T) {
			testRunBrowserSetupSelectedMediaUsesConfiguredRangeRetention(t, storageMode)
		})
	}
}

func TestRunBrowserSetupRestoresMixedProviderManifest(t *testing.T) {
	root := t.TempDir()
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			require.Equal(t, "Bearer browser-token", request.Header.Get("Authorization"))
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"name":"TorBox.mp4","size":16,"hash":"fixture-hash","zipped":false,"infected":false}]}}`))
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, provider.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			writer.Header().Set("Content-Range", "bytes 0-0/16")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write([]byte("0"))
			require.NoError(t, err)
		case "/metadata/fixture":
			_, err := writer.Write([]byte(`{
				"metadata":{"licenseurl":"https://creativecommons.org/licenses/by/4.0/"},
				"files":[{"name":"Archive.mp4","size":16,"sha1":"1111111111111111111111111111111111111111","source":"original"}]
			}`))
			require.NoError(t, err)
		case "/download/fixture/Archive.mp4":
			writer.Header().Set("Content-Length", "16")
			writer.Header().Set("ETag", `"archive-etag"`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(provider.Close)

	archiveGateway, err := internetarchive.New(provider.URL+"/", provider.Client())
	require.NoError(t, err)
	release, err := acquisitiondomain.NewRelease(acquisitiondomain.ReleaseInput{
		Provider: "internet-archive", SourceID: "fixture", Title: "Archive Movie (2026)",
		Protocol: acquisitiondomain.ReleaseProtocolTorrent, Size: 16, Indexer: "Internet Archive",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	require.NoError(t, err)
	archiveCandidates, err := archiveGateway.ListRangeCandidates(context.Background(), release)
	require.NoError(t, err)
	require.Len(t, archiveCandidates, 1)
	torboxCandidate, err := domain.NewMediaCandidate("17:3", "TorBox.mp4", 16)
	require.NoError(t, err)
	torboxConfiguration, err := domain.NewSetupConfiguration(torboxCandidate, "TorBox Movie", 2026)
	require.NoError(t, err)
	archiveConfiguration, err := domain.NewSetupConfiguration(archiveCandidates[0].Media(), "Archive Movie", 2026)
	require.NoError(t, err)
	manifest, err := domain.NewSetupManifest([]domain.SetupConfiguration{torboxConfiguration, archiveConfiguration})
	require.NoError(t, err)

	cfg := testConfig(root, "")
	cfg.StorageMode = domain.StorageModeRolling
	cfg.CacheMaxBytes = 1024
	cfg.CacheChunkBytes = 256
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = provider.URL + "/v1/api/"
	cfg.OpenMediaSearchEnabled = true
	cfg.OpenMediaSearchURL = provider.URL + "/"
	cfg.RangeTimeout = 5 * time.Second
	setupRepository, err := setuprepo.New(cfg.SetupDir)
	require.NoError(t, err)
	require.NoError(t, setupRepository.SaveManifest(context.Background(), "browser-token", manifest))

	var activeCatalog nfsCatalog
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpStarted := make(chan struct{}, 1)
	deps := defaultDependencies()
	deps.httpClient = provider.Client()
	deps.torBoxClient = provider.Client()
	deps.serveNFS = func(_ context.Context, _ string, catalog nfsCatalog) (nfsServer, error) {
		activeCatalog = catalog
		return nfs, nil
	}
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, listenErr := net.Listen(network, address)
		if listenErr == nil {
			httpStarted <- struct{}{}
		}
		return listener, listenErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	select {
	case <-httpStarted:
	case runErr := <-result:
		require.NoError(t, runErr)
		require.FailNow(t, "mixed-provider setup stopped before HTTP startup")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "mixed-provider setup did not start HTTP")
	}
	require.Eventually(t, func() bool {
		items, listErr := activeCatalog.List(context.Background())
		if listErr != nil || len(items) != 2 {
			return false
		}
		for _, item := range items {
			if item.Backing.Provider == internetarchive.FileProviderName {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)

	cancel()
	require.NoError(t, <-result)
}

func testRunBrowserSetupSelectedMediaUsesConfiguredRangeRetention(t *testing.T, storageMode domain.StorageMode) {
	root := t.TempDir()
	content := []byte("0123456789abcdef")
	var contentRangeCalls atomic.Int32
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/api/torrents/mylist":
			require.Equal(t, "Bearer browser-token", request.Header.Get("Authorization"))
			writer.Header().Set("Content-Type", "application/json")
			if request.URL.Query().Get("id") == "17" {
				_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"name":"Example.mp4","size":16,"hash":"fixture-hash","zipped":false,"infected":false}]}}`))
				require.NoError(t, err)
				return
			}
			_, err := writer.Write([]byte(`{"success":true,"detail":"ok","data":[{"id":17,"download_finished":true,"download_present":true,"files":[{"id":3,"name":"Example.mp4","size":16,"hash":"fixture-hash","zipped":false,"infected":false}]}]}`))
			require.NoError(t, err)
		case "/v1/api/torrents/requestdl":
			_, err := fmt.Fprintf(writer, `{"success":true,"detail":"ok","data":%q}`, provider.URL+"/cdn/file")
			require.NoError(t, err)
		case "/cdn/file":
			require.Equal(t, http.MethodGet, request.Method)
			start, end := 8, 11
			if request.Header.Get("Range") == "bytes=0-0" {
				start, end = 0, 0
			} else {
				require.Equal(t, "bytes=8-11", request.Header.Get("Range"))
				contentRangeCalls.Add(1)
			}
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/16", start, end))
			writer.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
			writer.WriteHeader(http.StatusPartialContent)
			_, err := writer.Write(content[start : end+1])
			require.NoError(t, err)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := testConfig(root, "")
	cfg.StorageMode = storageMode
	if storageMode == domain.StorageModeRolling {
		cfg.CacheMaxBytes = 8
	} else {
		cfg.CacheMaxBytes = 0
	}
	cfg.CacheChunkBytes = 4
	cfg.RangeProvider = "torbox-torrent"
	cfg.FilesystemMode = "nfs"
	cfg.SetupEnabled = true
	cfg.SetupDir = filepath.Join(root, "data", "setup")
	cfg.SetupBootstrapToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.TorBoxAPIURL = provider.URL + "/v1/api/"
	cfg.RangeTimeout = time.Second

	var activeCatalog nfsCatalog
	nfs := &fakeNFSServer{address: fakeAddress("127.0.0.1:2049")}
	httpAddress := make(chan string, 1)
	deps := defaultDependencies()
	deps.torBoxClient = provider.Client()
	deps.serveNFS = func(_ context.Context, _ string, catalog nfsCatalog) (nfsServer, error) {
		activeCatalog = catalog
		return nfs, nil
	}
	deps.listen = func(network string, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			httpAddress <- listener.Addr().String()
		}
		return listener, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, testLogger(), deps) }()
	var address string
	select {
	case address = <-httpAddress:
	case runErr := <-result:
		require.NoError(t, runErr)
		require.FailNow(t, "browser setup stopped before HTTP startup")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "browser setup did not start HTTP")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	statusResponse, err := client.Get("http://" + address + "/api/setup/status")
	require.NoError(t, err)
	var status struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.NewDecoder(statusResponse.Body).Decode(&status))
	require.NoError(t, statusResponse.Body.Close())
	require.NotEmpty(t, status.CSRFToken)

	payload := bytes.NewBufferString(`{"token":"browser-token","objectId":"17:3","title":"Example","year":2026}`)
	request, err := http.NewRequest(http.MethodPut, "http://"+address+"/api/setup/configuration", payload)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+address)
	request.Header.Set("X-BlackPearl-CSRF", status.CSRFToken)
	request.Header.Set("X-BlackPearl-Bootstrap", cfg.SetupBootstrapToken)
	applyResponse, err := client.Do(request)
	require.NoError(t, err)
	applyBody, err := io.ReadAll(applyResponse.Body)
	require.NoError(t, err)
	require.NoError(t, applyResponse.Body.Close())
	require.Equal(t, http.StatusOK, applyResponse.StatusCode, string(applyBody))

	items, err := activeCatalog.List(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	handle, err := activeCatalog.Open(context.Background(), items[0].VirtualPath)
	require.NoError(t, err)
	buffer := make([]byte, 4)
	read, err := handle.ReadAt(context.Background(), buffer, 8)
	require.NoError(t, err)
	require.Equal(t, 4, read)
	require.Equal(t, content[8:12], buffer)
	require.NoError(t, handle.Close())
	require.Equal(t, int32(1), contentRangeCalls.Load())

	secondRequest, err := http.NewRequest(http.MethodPut, "http://"+address+"/api/setup/configuration", bytes.NewBufferString(`{"token":"browser-token","objectId":"17:3","title":"Example","year":2026}`))
	require.NoError(t, err)
	secondRequest.Header.Set("Content-Type", "application/json")
	secondRequest.Header.Set("Origin", "http://"+address)
	secondRequest.Header.Set("X-BlackPearl-CSRF", status.CSRFToken)
	secondRequest.Header.Set("X-BlackPearl-Bootstrap", cfg.SetupBootstrapToken)
	secondResponse, err := client.Do(secondRequest)
	require.NoError(t, err)
	secondBody, err := io.ReadAll(secondResponse.Body)
	require.NoError(t, err)
	require.NoError(t, secondResponse.Body.Close())
	require.Equal(t, http.StatusOK, secondResponse.StatusCode, string(secondBody))

	items, err = activeCatalog.List(context.Background())
	require.NoError(t, err)
	handle, err = activeCatalog.Open(context.Background(), items[0].VirtualPath)
	require.NoError(t, err)
	buffer = make([]byte, 4)
	_, err = handle.ReadAt(context.Background(), buffer, 8)
	require.NoError(t, err)
	require.Equal(t, content[8:12], buffer)
	require.NoError(t, handle.Close())
	require.Equal(t, int32(1), contentRangeCalls.Load())
	cacheNamespace := "rolling"
	if storageMode == domain.StorageModePersistent {
		cacheNamespace = "persistent"
	}
	require.DirExists(t, filepath.Join(cfg.CacheDir, cacheNamespace))

	cancel()
	require.NoError(t, <-result)
}

func TestDefaultDependenciesKeepTorBoxTrafficOutOfInstrumentedClient(t *testing.T) {
	t.Parallel()

	deps := defaultDependencies()

	require.NotEqual(t, fmt.Sprintf("%T", deps.httpClient.Transport), fmt.Sprintf("%T", deps.torBoxClient.Transport))
	require.Equal(t, fmt.Sprintf("%T", http.DefaultTransport), fmt.Sprintf("%T", deps.torBoxClient.Transport))
}

func TestResolveTorBoxTokenReadsDockerSecretWithoutReturningNewline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "torbox-token")
	require.NoError(t, os.WriteFile(path, []byte("file-secret\n"), 0o600))
	cfg := config.Config{TorBoxAPITokenFile: path}

	token, err := resolveTorBoxToken(context.Background(), cfg)

	require.NoError(t, err)
	require.Equal(t, "file-secret", token)
}

func TestResolveTorBoxTokenRejectsUnsafeSecretFilesWithoutExposingContents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
	}{
		{name: "surrounding whitespace", content: []byte(" secret-value\n")},
		{name: "oversized", content: bytes.Repeat([]byte("x"), 4097)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "torbox-token")
			require.NoError(t, os.WriteFile(path, test.content, 0o600))

			_, err := resolveTorBoxToken(context.Background(), config.Config{TorBoxAPITokenFile: path})

			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-value")
		})
	}
}

func TestRunValidatesModeAndDependenciesBeforeCreatingPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name    string
		mutate  func(*config.Config, *dependencies)
		message string
	}{
		{
			name: "unknown storage mode",
			mutate: func(cfg *config.Config, _ *dependencies) {
				cfg.StorageMode = "archive"
			},
			message: "unsupported storage mode",
		},
		{
			name: "missing mount",
			mutate: func(_ *config.Config, deps *dependencies) {
				deps.mount = nil
			},
			message: "mount dependency is required",
		},
		{
			name: "missing listener",
			mutate: func(_ *config.Config, deps *dependencies) {
				deps.listen = nil
			},
			message: "listener dependency is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			caseRoot := filepath.Join(root, test.name)
			cfg := testConfig(caseRoot, "")
			deps := defaultDependencies()
			test.mutate(&cfg, &deps)

			err := run(context.Background(), cfg, testLogger(), deps)

			require.ErrorContains(t, err, test.message)
			_, statErr := os.Stat(cfg.DataDir)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestRunUnmountsWhenDiagnosticsListenerFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := testConfig(root, "")
	server := &fakeMountServer{}
	deps := dependencies{
		mount: func(context.Context, string, *pearlfs.Root) (mountServer, error) {
			return server, nil
		},
		listen: func(string, string) (net.Listener, error) {
			return nil, errors.New("address unavailable")
		},
	}

	err := run(context.Background(), cfg, testLogger(), deps)

	require.ErrorContains(t, err, "listen on diagnostics address")
	require.True(t, server.unmounted)
}

func TestRunReportsDirectoryCreationFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("file"), 0o600))
	cfg := testConfig(root, "")
	cfg.DataDir = filepath.Join(blocked, "data")

	err := run(context.Background(), cfg, testLogger(), defaultDependencies())

	require.ErrorContains(t, err, "create service directory")
}

func TestReadinessGateRequiresMountAndDelegates(t *testing.T) {
	t.Parallel()
	delegate := &fakeReadyCatalog{err: errors.New("cache unavailable")}
	gate := &readinessGate{delegate: delegate}

	err := gate.Ready(context.Background())
	require.ErrorContains(t, err, "not mounted")
	gate.ready.Store(true)
	err = gate.Ready(context.Background())
	require.ErrorContains(t, err, "cache unavailable")
	require.True(t, delegate.called)
}

func TestStartSetupRestoreRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	restorer := &fakeSetupRestorer{results: []error{setupservice.ErrUnavailable, nil}, calls: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	startSetupRestore(ctx, restorer, testLogger(), time.Millisecond)

	for range 2 {
		select {
		case <-restorer.calls:
		case <-time.After(time.Second):
			require.FailNow(t, "saved setup restore was not retried")
		}
	}
}

func TestStartSetupRestoreDoesNotRetryMissingState(t *testing.T) {
	t.Parallel()
	restorer := &fakeSetupRestorer{results: []error{domain.ErrNotFound}, calls: make(chan struct{}, 2)}

	startSetupRestore(context.Background(), restorer, testLogger(), time.Millisecond)

	select {
	case <-restorer.calls:
	case <-time.After(time.Second):
		require.FailNow(t, "initial saved setup restore did not run")
	}
	select {
	case <-restorer.calls:
		require.FailNow(t, "missing setup state was retried")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestStartSetupRestoreDoesNotRetryPermanentFailure(t *testing.T) {
	t.Parallel()
	restorer := &fakeSetupRestorer{results: []error{errors.New("invalid saved setup")}, calls: make(chan struct{}, 2)}

	startSetupRestore(context.Background(), restorer, testLogger(), time.Millisecond)

	select {
	case <-restorer.calls:
	case <-time.After(time.Second):
		require.FailNow(t, "initial saved setup restore did not run")
	}
	select {
	case <-restorer.calls:
		require.FailNow(t, "permanent setup failure was retried")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestStartSetupRestoreStopsRetryingAfterShutdown(t *testing.T) {
	t.Parallel()
	restorer := &fakeSetupRestorer{results: []error{setupservice.ErrUnavailable}, calls: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())

	startSetupRestore(ctx, restorer, testLogger(), time.Hour)
	cancel()

	select {
	case <-restorer.calls:
	case <-time.After(time.Second):
		require.FailNow(t, "initial saved setup restore did not run")
	}
	select {
	case <-restorer.calls:
		require.FailNow(t, "saved setup restore retried after shutdown")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDefaultDependenciesAreComplete(t *testing.T) {
	t.Parallel()

	deps := defaultDependencies()

	require.NotNil(t, deps.mount)
	require.NotNil(t, deps.serveNFS)
	require.NotNil(t, deps.listen)
	require.NotNil(t, deps.httpClient)
}

func TestExecuteLoadsConfigurationAndShutsDownTelemetry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BLACKPEARL_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("BLACKPEARL_DB_PATH", filepath.Join(root, "data", "blackpearl.db"))
	t.Setenv("BLACKPEARL_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("BLACKPEARL_MOUNT_PATH", filepath.Join(root, "mount"))
	t.Setenv("BLACKPEARL_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("BLACKPEARL_LOG_LEVEL", "error")
	t.Setenv("BLACKPEARL_STORAGE_MODE", "rolling")
	t.Setenv("BLACKPEARL_CACHE_MAX_BYTES", "1048576")
	t.Setenv("BLACKPEARL_CACHE_CHUNK_BYTES", "262144")
	t.Setenv("BLACKPEARL_RANGE_ORIGIN_URL", "http://127.0.0.1:1/media/")
	t.Setenv("BLACKPEARL_RANGE_OBJECT_ID", "movie.mp4")
	t.Setenv("BLACKPEARL_RANGE_TIMEOUT", "100ms")
	t.Setenv("BLACKPEARL_POC_SOURCE", "")
	t.Setenv("BLACKPEARL_PLEX_URL", "")
	t.Setenv("BLACKPEARL_PLEX_TOKEN", "")
	t.Setenv("BLACKPEARL_PLEX_SECTION_ID", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	err := execute(context.Background())

	require.ErrorContains(t, err, "range object metadata")
	_, statErr := os.Stat(filepath.Join(root, "data"))
	require.NoError(t, statErr)
}

func TestExecuteReportsConfigurationFailure(t *testing.T) {
	t.Setenv("BLACKPEARL_STORAGE_MODE", "unknown")

	err := execute(context.Background())

	require.ErrorContains(t, err, "STORAGE_MODE")
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}

type fakeMountServer struct {
	unmounted bool
}

func (f *fakeMountServer) Unmount() error {
	f.unmounted = true
	return nil
}

func (f *fakeMountServer) Wait() {}

type fakeReadyCatalog struct {
	err    error
	called bool
}

func (f *fakeReadyCatalog) List(context.Context) ([]domain.Media, error) {
	return []domain.Media{}, nil
}

func (f *fakeReadyCatalog) Open(context.Context, string) (domain.ReadHandle, error) {
	return nil, domain.ErrNotFound
}

type fakePublicationNotifier struct {
	calls atomic.Int32
}

func (f *fakePublicationNotifier) Notify() {
	f.calls.Add(1)
}

type fakeSetupRestorer struct {
	mu      sync.Mutex
	results []error
	calls   chan struct{}
}

func (f *fakeSetupRestorer) Restore(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls <- struct{}{}
	if len(f.results) == 0 {
		return nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

type fakeNFSServer struct {
	address    net.Addr
	closed     bool
	waited     bool
	replaceErr error
}

func (f *fakeNFSServer) Addr() net.Addr {
	return f.address
}

func (f *fakeNFSServer) Reload(context.Context) error {
	return nil
}

func (f *fakeNFSServer) Replace(context.Context, nfsCatalog) (nfsCatalog, error) {
	return nil, f.replaceErr
}

func (f *fakeNFSServer) Close() error {
	f.closed = true
	return nil
}

func (f *fakeNFSServer) Wait() error {
	f.waited = true
	return nil
}

type fakeAddress string

func (a fakeAddress) Network() string {
	return "tcp"
}

func (a fakeAddress) String() string {
	return string(a)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (f *fakeReadyCatalog) Ready(context.Context) error {
	f.called = true
	return f.err
}
