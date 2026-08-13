package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/cache"
	"github.com/blackpearl-media/blackpearl/internal/config"
	"github.com/blackpearl-media/blackpearl/internal/core"
	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/blackpearl-media/blackpearl/internal/httpserver"
	"github.com/blackpearl-media/blackpearl/internal/pearlfs"
	"github.com/blackpearl-media/blackpearl/internal/plex"
	"github.com/blackpearl-media/blackpearl/internal/state"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type mountServer interface {
	Unmount() error
	Wait()
}

type dependencies struct {
	mount      func(context.Context, string, *pearlfs.Root) (mountServer, error)
	listen     func(network string, address string) (net.Listener, error)
	httpClient *http.Client
}

func defaultDependencies() dependencies {
	return dependencies{
		mount: func(ctx context.Context, mountPath string, root *pearlfs.Root) (mountServer, error) {
			return pearlfs.Mount(ctx, mountPath, root)
		},
		listen: net.Listen,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger, deps dependencies) (runErr error) {
	switch cfg.StorageMode {
	case domain.StorageModePersistent:
	case domain.StorageModeRolling:
		return fmt.Errorf("%w: rolling storage mode", domain.ErrNotConfigured)
	default:
		return fmt.Errorf("unsupported storage mode: %q", cfg.StorageMode)
	}
	if deps.mount == nil {
		return errors.New("mount dependency is required")
	}
	if deps.listen == nil {
		return errors.New("listener dependency is required")
	}
	if deps.httpClient == nil {
		deps.httpClient = defaultDependencies().httpClient
	}
	for _, directory := range []string{cfg.DataDir, cfg.CacheDir, cfg.MountPath} {
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
	cacheStore, err := cache.New(cfg.CacheDir)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	catalog := core.NewCatalog(repository, cacheStore, cacheStore)
	if cfg.POCSource != "" {
		media, importErr := catalog.ImportPOC(ctx, cfg.POCSource)
		if importErr != nil {
			return importErr
		}
		logger.InfoContext(ctx, "imported POC fixture", "mediaId", media.ID, "virtualPath", media.VirtualPath, "sizeBytes", media.Size)
	}
	root, err := pearlfs.New(ctx, catalog)
	if err != nil {
		return err
	}
	fuseServer, err := deps.mount(ctx, cfg.MountPath, root)
	if err != nil {
		return fmt.Errorf("mount PearlFS: %w", err)
	}
	defer func() {
		unmountErr := fuseServer.Unmount()
		if unmountErr == nil {
			fuseServer.Wait()
		} else {
			runErr = errors.Join(runErr, fmt.Errorf("unmount PearlFS: %w", unmountErr))
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
	logger.InfoContext(ctx, "BlackPearl ready", "httpAddress", listener.Addr().String(), "mountPath", cfg.MountPath)

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
