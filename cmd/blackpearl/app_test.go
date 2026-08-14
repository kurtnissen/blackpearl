package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/pearlfs"
	"github.com/blackpearl-media/blackpearl/internal/state"
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

func testConfig(root string, source string) config.Config {
	return config.Config{
		DataDir:         filepath.Join(root, "data"),
		DBPath:          filepath.Join(root, "data", "blackpearl.db"),
		CacheDir:        filepath.Join(root, "data", "cache"),
		MountPath:       filepath.Join(root, "mount"),
		POCSource:       source,
		HTTPAddr:        "127.0.0.1:0",
		LogLevel:        "debug",
		StorageMode:     domain.StorageModePersistent,
		CacheChunkBytes: 262_144,
		RangeTimeout:    30 * time.Second,
		FilesystemMode:  "fuse",
		NFSAddr:         "127.0.0.1:0",
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
			_, err := writer.Write([]byte(fmt.Sprintf(`{"success":true,"detail":"ok","data":%q}`, api.URL+"/cdn/file")))
			require.NoError(t, err)
		case "/cdn/file":
			require.Empty(t, request.Header.Get("Authorization"))
			require.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Length", "16")
			writer.Header().Set("Accept-Ranges", "bytes")
			writer.WriteHeader(http.StatusOK)
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
	deps.httpClient = api.Client()
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

type fakeNFSServer struct {
	address net.Addr
	closed  bool
	waited  bool
}

func (f *fakeNFSServer) Addr() net.Addr {
	return f.address
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

func (f *fakeReadyCatalog) Ready(context.Context) error {
	f.called = true
	return f.err
}
