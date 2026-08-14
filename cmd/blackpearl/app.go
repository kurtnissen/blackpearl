package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/gateway/httporigin"
	"github.com/blackpearl-media/blackpearl/internal/gateway/torbox"
	"github.com/blackpearl-media/blackpearl/internal/httpserver"
	"github.com/blackpearl-media/blackpearl/internal/pearlfs"
	"github.com/blackpearl-media/blackpearl/internal/pearlnfs"
	"github.com/blackpearl-media/blackpearl/internal/plex"
	"github.com/blackpearl-media/blackpearl/internal/state"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type mountServer interface {
	Unmount() error
	Wait()
}

type nfsCatalog = pearlnfs.Catalog

type nfsServer interface {
	Addr() net.Addr
	Close() error
	Wait() error
}

type dependencies struct {
	mount      func(context.Context, string, *pearlfs.Root) (mountServer, error)
	serveNFS   func(context.Context, string, nfsCatalog) (nfsServer, error)
	listen     func(network string, address string) (net.Listener, error)
	httpClient *http.Client
	// torBoxClient intentionally has no automatic HTTP instrumentation because
	// TorBox requires secrets in request and signed-URL query strings.
	torBoxClient *http.Client
}

func defaultDependencies() dependencies {
	return dependencies{
		mount: func(ctx context.Context, mountPath string, root *pearlfs.Root) (mountServer, error) {
			return pearlfs.Mount(ctx, mountPath, root)
		},
		serveNFS: func(ctx context.Context, address string, catalog nfsCatalog) (nfsServer, error) {
			filesystem, err := pearlnfs.New(ctx, catalog)
			if err != nil {
				return nil, err
			}
			return pearlnfs.Start(ctx, address, filesystem)
		},
		listen: net.Listen,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		torBoxClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: http.DefaultTransport,
		},
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger, deps dependencies) (runErr error) {
	switch cfg.StorageMode {
	case domain.StorageModePersistent:
	case domain.StorageModeRolling:
	default:
		return fmt.Errorf("unsupported storage mode: %q", cfg.StorageMode)
	}
	switch cfg.FilesystemMode {
	case "fuse":
		if deps.mount == nil {
			return errors.New("mount dependency is required")
		}
	case "nfs":
		if deps.serveNFS == nil {
			return errors.New("NFS server dependency is required")
		}
	default:
		return fmt.Errorf("unsupported filesystem mode: %q", cfg.FilesystemMode)
	}
	if deps.listen == nil {
		return errors.New("listener dependency is required")
	}
	if deps.httpClient == nil {
		deps.httpClient = defaultDependencies().httpClient
	}
	if deps.torBoxClient == nil {
		deps.torBoxClient = defaultDependencies().torBoxClient
	}
	directories := []string{cfg.DataDir, cfg.CacheDir}
	if cfg.FilesystemMode == "fuse" {
		directories = append(directories, cfg.MountPath)
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create service directory %s: %w", directory, err)
		}
	}

	repository, err := state.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open catalog state: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, repository.Close())
	}()
	var catalog *core.Catalog
	switch cfg.StorageMode {
	case domain.StorageModePersistent:
		cacheStore, cacheErr := cache.New(cfg.CacheDir)
		if cacheErr != nil {
			return fmt.Errorf("open cache: %w", cacheErr)
		}
		catalog = core.NewCatalog(repository, cacheStore, cacheStore)
		if cfg.POCSource != "" {
			media, importErr := catalog.ImportPOC(ctx, cfg.POCSource)
			if importErr != nil {
				return importErr
			}
			logger.InfoContext(ctx, "imported POC fixture", "mediaId", media.ID, "virtualPath", media.VirtualPath, "sizeBytes", media.Size)
		}
	case domain.StorageModeRolling:
		rangeClient := *deps.httpClient
		rangeClient.Timeout = cfg.RangeTimeout
		var gateway cache.RangeOpener
		switch cfg.RangeProvider {
		case "", "http-range":
			httpGateway, gatewayErr := httporigin.New(cfg.RangeOriginURL, &rangeClient)
			if gatewayErr != nil {
				return fmt.Errorf("configure HTTP range gateway: %w", gatewayErr)
			}
			gateway = httpGateway
		case "torbox-torrent":
			torBoxClient := *deps.torBoxClient
			torBoxClient.Timeout = cfg.RangeTimeout
			torBoxToken, tokenErr := resolveTorBoxToken(ctx, cfg)
			if tokenErr != nil {
				return tokenErr
			}
			torboxGateway, gatewayErr := torbox.New(torbox.Options{
				APIBaseURL:  cfg.TorBoxAPIURL,
				APIToken:    torBoxToken,
				MetadataTTL: time.Minute,
				LinkTTL:     2 * time.Hour,
			}, &torBoxClient)
			if gatewayErr != nil {
				return fmt.Errorf("configure TorBox range gateway: %w", gatewayErr)
			}
			gateway = torboxGateway
		default:
			return fmt.Errorf("unsupported range provider: %q", cfg.RangeProvider)
		}
		rollingSource, rollingErr := cache.NewRolling(ctx, cache.RollingOptions{
			Root:         cfg.CacheDir,
			MaxBytes:     cfg.CacheMaxBytes,
			ChunkBytes:   cfg.CacheChunkBytes,
			FetchTimeout: cfg.RangeTimeout,
		}, gateway)
		if rollingErr != nil {
			return fmt.Errorf("open rolling cache: %w", rollingErr)
		}
		providerName := cfg.RangeProvider
		if providerName == "" {
			providerName = "http-range"
		}
		backing, backingErr := domain.NewBackingRef(providerName, cfg.RangeObjectID)
		if backingErr != nil {
			return fmt.Errorf("construct rolling POC backing: %w", backingErr)
		}
		metadataSource, openErr := gateway.Open(ctx, backing)
		if openErr != nil {
			return fmt.Errorf("open rolling POC metadata: %w", openErr)
		}
		logicalSize := metadataSource.Size()
		if closeErr := metadataSource.Close(); closeErr != nil {
			return fmt.Errorf("close rolling POC metadata: %w", closeErr)
		}
		catalog = core.NewCatalog(repository, nil, rollingSource)
		media, registerErr := catalog.RegisterPOC(ctx, backing, logicalSize)
		if registerErr != nil {
			return registerErr
		}
		logger.InfoContext(ctx, "registered rolling POC", "mediaId", media.ID, "virtualPath", media.VirtualPath, "sizeBytes", media.Size)
	}
	var stopFilesystem func() error
	var waitFilesystem func() error
	var filesystemEndpoint string
	switch cfg.FilesystemMode {
	case "fuse":
		root, rootErr := pearlfs.New(ctx, catalog)
		if rootErr != nil {
			return rootErr
		}
		fuseServer, mountErr := deps.mount(ctx, cfg.MountPath, root)
		if mountErr != nil {
			return fmt.Errorf("mount PearlFS: %w", mountErr)
		}
		stopFilesystem = fuseServer.Unmount
		waitFilesystem = func() error {
			fuseServer.Wait()
			return nil
		}
		filesystemEndpoint = cfg.MountPath
	case "nfs":
		server, startErr := deps.serveNFS(ctx, cfg.NFSAddr, catalog)
		if startErr != nil {
			return fmt.Errorf("start PearlNFS: %w", startErr)
		}
		stopFilesystem = server.Close
		waitFilesystem = server.Wait
		filesystemEndpoint = server.Addr().String()
	}
	defer func() {
		stopErr := stopFilesystem()
		if stopErr == nil {
			runErr = errors.Join(runErr, waitFilesystem())
		} else {
			runErr = errors.Join(runErr, fmt.Errorf("stop %s filesystem: %w", cfg.FilesystemMode, stopErr))
		}
	}()

	gate := &readinessGate{delegate: catalog}
	handler := otelhttp.NewHandler(httpserver.New(gate, logger), "blackpearl.diagnostics")
	listener, err := deps.listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on diagnostics address %s: %w", cfg.HTTPAddr, err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	gate.ready.Store(true)
	defer func() {
		gate.ready.Store(false)
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shut down diagnostics HTTP: %w", err))
		}
	}()
	logger.InfoContext(ctx, "BlackPearl ready", "httpAddress", listener.Addr().String(), "filesystemMode", cfg.FilesystemMode, "filesystemEndpoint", filesystemEndpoint)

	if cfg.Plex.Enabled() {
		gateway, gatewayErr := plex.New(cfg.Plex.URL, cfg.Plex.Token, cfg.Plex.SectionID, deps.httpClient)
		if gatewayErr != nil {
			return fmt.Errorf("configure Plex gateway: %w", gatewayErr)
		}
		if refreshErr := gateway.Refresh(ctx); refreshErr != nil {
			logger.WarnContext(ctx, "Plex library refresh failed", "error", refreshErr)
		}
	}

	select {
	case <-ctx.Done():
	case serveErr := <-serveErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			runErr = fmt.Errorf("serve diagnostics HTTP: %w", serveErr)
		}
	}
	return runErr
}

func resolveTorBoxToken(ctx context.Context, cfg config.Config) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("read TorBox API token: %w", err)
	}
	if cfg.TorBoxAPITokenFile == "" {
		return cfg.TorBoxAPIToken, nil
	}
	file, err := os.Open(cfg.TorBoxAPITokenFile)
	if err != nil {
		return "", errors.New("open BLACKPEARL_TORBOX_API_TOKEN_FILE")
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", errors.New("read BLACKPEARL_TORBOX_API_TOKEN_FILE")
	}
	if len(content) > 4096 {
		return "", errors.New("BLACKPEARL_TORBOX_API_TOKEN_FILE exceeds 4096 bytes")
	}
	token := strings.TrimSuffix(string(content), "\n")
	if token == "" || strings.TrimSpace(token) != token {
		return "", errors.New("BLACKPEARL_TORBOX_API_TOKEN_FILE must contain one token without surrounding whitespace")
	}
	return token, nil
}

type readyCatalog interface {
	Ready(context.Context) error
}

type readinessGate struct {
	delegate readyCatalog
	ready    atomic.Bool
}

func (g *readinessGate) Ready(ctx context.Context) error {
	if !g.ready.Load() {
		return errors.New("service is not mounted")
	}
	return g.delegate.Ready(ctx)
}
