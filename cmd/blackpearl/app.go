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
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	acquisitiondomain "github.com/kurtnissen/blackpearl/internal/acquisition"
	"github.com/kurtnissen/blackpearl/internal/cache"
	"github.com/kurtnissen/blackpearl/internal/config"
	"github.com/kurtnissen/blackpearl/internal/core"
	"github.com/kurtnissen/blackpearl/internal/domain"
	"github.com/kurtnissen/blackpearl/internal/gateway/httporigin"
	"github.com/kurtnissen/blackpearl/internal/gateway/internetarchive"
	"github.com/kurtnissen/blackpearl/internal/gateway/plexmetadata"
	"github.com/kurtnissen/blackpearl/internal/gateway/plexplayback"
	"github.com/kurtnissen/blackpearl/internal/gateway/plexwatchlist"
	"github.com/kurtnissen/blackpearl/internal/gateway/prowlarr"
	"github.com/kurtnissen/blackpearl/internal/gateway/torbox"
	setuphandler "github.com/kurtnissen/blackpearl/internal/handler/setup"
	"github.com/kurtnissen/blackpearl/internal/httpserver"
	"github.com/kurtnissen/blackpearl/internal/pearlfs"
	"github.com/kurtnissen/blackpearl/internal/pearlnfs"
	"github.com/kurtnissen/blackpearl/internal/plex"
	acquisitionrepo "github.com/kurtnissen/blackpearl/internal/repository/acquisition"
	acquisitionjobrepo "github.com/kurtnissen/blackpearl/internal/repository/acquisitionjob"
	setuprepo "github.com/kurtnissen/blackpearl/internal/repository/setup"
	watchlistrepo "github.com/kurtnissen/blackpearl/internal/repository/watchlist"
	"github.com/kurtnissen/blackpearl/internal/resolver"
	acquisitionservice "github.com/kurtnissen/blackpearl/internal/service/acquisition"
	acquisitionjobservice "github.com/kurtnissen/blackpearl/internal/service/acquisitionjob"
	directrangeservice "github.com/kurtnissen/blackpearl/internal/service/directrange"
	playbackadvanceservice "github.com/kurtnissen/blackpearl/internal/service/playbackadvance"
	plexrefreshservice "github.com/kurtnissen/blackpearl/internal/service/plexrefresh"
	setupservice "github.com/kurtnissen/blackpearl/internal/service/setup"
	watchlistservice "github.com/kurtnissen/blackpearl/internal/service/watchlist"
	"github.com/kurtnissen/blackpearl/internal/state"
	webui "github.com/kurtnissen/blackpearl/web"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
)

type mountServer interface {
	Unmount() error
	Wait()
}

type nfsCatalog = pearlnfs.Catalog

type nfsServer interface {
	Addr() net.Addr
	Reload(context.Context) error
	Replace(context.Context, nfsCatalog) (nfsCatalog, error)
	Close() error
	Wait() error
}

type publicationNotifier interface {
	Notify()
}

type materializerFunc func(context.Context, acquisitiondomain.Release) (acquisitiondomain.TorrentInput, error)

func (function materializerFunc) Materialize(ctx context.Context, release acquisitiondomain.Release) (acquisitiondomain.TorrentInput, error) {
	return function(ctx, release)
}

type setupPublisher struct {
	switcher *core.CatalogSwitch
	nfs      nfsServer
	notifier publicationNotifier
}

func (p *setupPublisher) Publish(ctx context.Context, next core.CatalogService) error {
	if _, err := p.nfs.Replace(ctx, next); err != nil {
		return err
	}
	p.switcher.Activate(next)
	if p.notifier != nil {
		p.notifier.Notify()
	}
	return nil
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
			filesystem, err := pearlnfs.NewReloadable(ctx, catalog)
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

	if cfg.SetupEnabled {
		return runBrowserSetup(ctx, cfg, logger, deps)
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
			Root:                      cfg.CacheDir,
			MaxBytes:                  cfg.CacheMaxBytes,
			ChunkBytes:                cfg.CacheChunkBytes,
			ReadAheadChunks:           cfg.CacheReadAheadChunks,
			NextEpisodePrefetchChunks: cfg.CacheNextEpisodeChunks,
			FetchTimeout:              cfg.RangeTimeout,
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

func runBrowserSetup(ctx context.Context, cfg config.Config, logger *slog.Logger, deps dependencies) (runErr error) {
	switcher := core.NewCatalogSwitch()
	nfs, err := deps.serveNFS(ctx, cfg.NFSAddr, switcher)
	if err != nil {
		return fmt.Errorf("start setup PearlNFS: %w", err)
	}
	defer func() {
		closeErr := nfs.Close()
		if closeErr == nil {
			runErr = errors.Join(runErr, nfs.Wait())
		} else {
			runErr = errors.Join(runErr, fmt.Errorf("stop setup NFS: %w", closeErr))
		}
	}()

	setupRepository, err := setuprepo.New(cfg.SetupDir)
	if err != nil {
		return fmt.Errorf("open browser setup repository: %w", err)
	}
	acquisitionRepository, err := acquisitionrepo.New(filepath.Join(cfg.SetupDir, "acquisition"))
	if err != nil {
		return fmt.Errorf("open browser acquisition repository: %w", err)
	}
	var plexTokenSource plexwatchlist.TokenSource
	if cfg.WatchlistEnabled {
		if cfg.WatchlistPreferencesPath != "" {
			plexTokenSource, err = plexwatchlist.NewPreferencesTokenSource(cfg.WatchlistPreferencesPath)
		} else {
			plexTokenSource, err = plexwatchlist.NewTokenFileSource(cfg.WatchlistTokenFile)
		}
		if err != nil {
			return fmt.Errorf("configure Plex credential source: %w", err)
		}
	}
	var rangePool *cache.RollingPool
	switch cfg.StorageMode {
	case domain.StorageModePersistent:
		rangePool, err = cache.NewPersistentRangePool(ctx, cache.PersistentRangeOptions{
			Root: cfg.CacheDir, ChunkBytes: cfg.CacheChunkBytes,
			ReadAheadChunks:           cfg.CacheReadAheadChunks,
			NextEpisodePrefetchChunks: cfg.CacheNextEpisodeChunks,
			FetchTimeout:              cfg.RangeTimeout,
		})
	case domain.StorageModeRolling:
		rangePool, err = cache.NewRollingPool(ctx, cache.RollingOptions{
			Root: cfg.CacheDir, MaxBytes: cfg.CacheMaxBytes,
			ChunkBytes: cfg.CacheChunkBytes, ReadAheadChunks: cfg.CacheReadAheadChunks,
			NextEpisodePrefetchChunks: cfg.CacheNextEpisodeChunks,
			FetchTimeout:              cfg.RangeTimeout,
		})
	default:
		return fmt.Errorf("unsupported browser storage mode: %q", cfg.StorageMode)
	}
	if err != nil {
		return fmt.Errorf("open shared browser range cache: %w", err)
	}
	newTorBoxGateway := func(token string) (*torbox.Gateway, error) {
		client := *deps.torBoxClient
		client.Timeout = cfg.RangeTimeout
		return torbox.New(torbox.Options{
			APIBaseURL: cfg.TorBoxAPIURL, APIToken: token,
			MetadataTTL: time.Minute, LinkTTL: 2 * time.Hour,
		}, &client)
	}
	var openMediaGateway *internetarchive.Gateway
	if cfg.OpenMediaSearchEnabled {
		client := *deps.httpClient
		client.Timeout = cfg.AcquisitionOperationTimeout
		openMediaGateway, err = internetarchive.New(cfg.OpenMediaSearchURL, &client)
		if err != nil {
			return fmt.Errorf("configure open-media search gateway: %w", err)
		}
	}
	gatewayFactory := func(token string) (setupservice.Discoverer, error) {
		return newTorBoxGateway(token)
	}
	runtimeFactory := func(runtimeContext context.Context, token string, manifest domain.SetupManifest) (core.CatalogService, error) {
		client := *deps.torBoxClient
		client.Timeout = cfg.RangeTimeout
		gateway, gatewayErr := torbox.New(torbox.Options{
			APIBaseURL: cfg.TorBoxAPIURL, APIToken: token,
			MetadataTTL: time.Minute, LinkTTL: 2 * time.Hour,
		}, &client)
		if gatewayErr != nil {
			return nil, fmt.Errorf("configure selected TorBox source: %w", gatewayErr)
		}
		openers := map[string]cache.RangeOpener{"torbox-torrent": gateway}
		if openMediaGateway != nil {
			openers[internetarchive.FileProviderName] = openMediaGateway
		}
		router, routerErr := cache.NewRangeRouter(openers)
		if routerErr != nil {
			return nil, fmt.Errorf("configure selected range providers: %w", routerErr)
		}
		rangeSource, rangeErr := rangePool.Source(router)
		if rangeErr != nil {
			return nil, fmt.Errorf("open selected range cache: %w", rangeErr)
		}
		catalog := core.NewCatalog(state.NewMemory(), nil, rangeSource)
		for index := range manifest.Items {
			configuration := manifest.Items[index]
			backing := configuration.Backing()
			metadata, openErr := router.Open(runtimeContext, backing)
			if openErr != nil {
				return nil, fmt.Errorf("validate selected range source: %w", openErr)
			}
			logicalSize := metadata.Size()
			if closeErr := metadata.Close(); closeErr != nil {
				return nil, fmt.Errorf("close selected range metadata: %w", closeErr)
			}
			if logicalSize != configuration.Size {
				return nil, fmt.Errorf("selected range size changed: got %d want %d", logicalSize, configuration.Size)
			}
			var registerErr error
			if configuration.MediaType == domain.MediaTypeEpisode {
				_, registerErr = catalog.RegisterRemoteEpisode(runtimeContext, configuration, backing)
			} else {
				_, registerErr = catalog.RegisterRemoteMovie(runtimeContext, configuration, backing)
			}
			if registerErr != nil {
				return nil, registerErr
			}
		}
		return catalog, nil
	}
	publisher := &setupPublisher{switcher: switcher, nfs: nfs}
	var plexRefreshWorker *plexrefreshservice.Worker
	if cfg.PlexRefreshEnabled {
		client := *deps.httpClient
		client.Timeout = min(cfg.RangeTimeout, 5*time.Second)
		refresher, refreshErr := plex.NewLibraryRefresher(
			cfg.PlexRefreshURL, plexTokenSource,
			[]string{"/blackpearl/Movies", "/blackpearl/TV Shows"},
			&client,
		)
		if refreshErr != nil {
			return fmt.Errorf("configure Plex library refresher: %w", refreshErr)
		}
		plexRefreshWorker, err = plexrefreshservice.New(refresher, plexrefreshservice.Options{
			Debounce: time.Second, RetryInterval: 5 * time.Second,
			OnError: func(refreshErr error) {
				logger.WarnContext(ctx, "Plex library refresh failed; retrying", "error", refreshErr)
			},
		})
		if err != nil {
			return fmt.Errorf("configure Plex library refresh worker: %w", err)
		}
		publisher.notifier = plexRefreshWorker
	}
	service := setupservice.New(setupRepository, gatewayFactory, runtimeFactory, publisher, cfg.SetupBootstrapToken)
	searchFactory := func(settings acquisitiondomain.SearchProviderSettings) (acquisitionservice.ReadySearchProvider, error) {
		if settings.Provider() != "prowlarr" {
			return nil, errors.New("unsupported acquisition search provider")
		}
		client := *deps.httpClient
		client.Timeout = cfg.AcquisitionOperationTimeout
		primary, gatewayErr := prowlarr.New(prowlarr.Options{BaseURL: settings.Endpoint(), APIKey: settings.Credential()}, &client)
		if gatewayErr != nil || openMediaGateway == nil {
			return primary, gatewayErr
		}
		return resolver.NewReadyPreferredSearcher(primary, openMediaGateway)
	}
	cachedGatewayFactory := func(token string) (acquisitionservice.CachedGateway, error) {
		return newTorBoxGateway(token)
	}
	acquisitionCoordinator, err := acquisitionservice.NewCoordinator(
		acquisitionRepository, setupRepository, searchFactory, cachedGatewayFactory, service,
		acquisitionservice.Options{InspectionAttempts: 8, InspectionInterval: 250 * time.Millisecond},
	)
	if err != nil {
		return fmt.Errorf("configure browser acquisition coordinator: %w", err)
	}
	acquisitionJobRepository, err := acquisitionjobrepo.Open(ctx, filepath.Join(cfg.DataDir, "acquisition-jobs.db"))
	if err != nil {
		return fmt.Errorf("open background acquisition queue: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, acquisitionJobRepository.Close()) }()
	acquisitionJobManager, err := acquisitionjobservice.NewManager(acquisitionJobRepository, acquisitionjobservice.ManagerOptions{})
	if err != nil {
		return fmt.Errorf("configure background acquisition manager: %w", err)
	}
	var directResolver acquisitionjobservice.DirectResolver
	var rangePreparer acquisitionjobservice.RangePreparer
	if openMediaGateway != nil {
		configuredResolver, resolverErr := directrangeservice.NewResolver(openMediaGateway)
		if resolverErr != nil {
			return fmt.Errorf("configure direct range resolver: %w", resolverErr)
		}
		configuredPreparer, preparerErr := directrangeservice.NewPreparer(openMediaGateway)
		if preparerErr != nil {
			return fmt.Errorf("configure direct range preparer: %w", preparerErr)
		}
		directResolver = configuredResolver
		rangePreparer = configuredPreparer
	}
	jobProviderFactory := func(factoryContext context.Context) (acquisitionjobservice.Providers, error) {
		settings, loadErr := acquisitionRepository.Load(factoryContext)
		if loadErr != nil {
			return acquisitionjobservice.Providers{}, fmt.Errorf("load background search settings: %w", loadErr)
		}
		token, _, loadErr := setupRepository.LoadManifest(factoryContext)
		if loadErr != nil {
			return acquisitionjobservice.Providers{}, fmt.Errorf("load background account settings: %w", loadErr)
		}
		client := *deps.httpClient
		client.Timeout = cfg.AcquisitionOperationTimeout
		searchGateway, gatewayErr := prowlarr.New(prowlarr.Options{BaseURL: settings.Endpoint(), APIKey: settings.Credential()}, &client)
		if gatewayErr != nil {
			return acquisitionjobservice.Providers{}, fmt.Errorf("configure background search gateway: %w", gatewayErr)
		}
		preparationGateway, gatewayErr := newTorBoxGateway(token)
		if gatewayErr != nil {
			return acquisitionjobservice.Providers{}, fmt.Errorf("configure background preparation gateway: %w", gatewayErr)
		}
		var jobSearcher acquisitionjobservice.Searcher = resolver.NewSearcher(searchGateway)
		var materializer acquisitionjobservice.Materializer = searchGateway
		if openMediaGateway != nil {
			jobSearcher = resolver.NewSearcher(openMediaGateway, searchGateway)
			materializer = materializerFunc(func(materialContext context.Context, release acquisitiondomain.Release) (acquisitiondomain.TorrentInput, error) {
				if release.Provider() == openMediaGateway.Name() {
					return openMediaGateway.Materialize(materialContext, release)
				}
				return searchGateway.Materialize(materialContext, release)
			})
		}
		return acquisitionjobservice.Providers{
			Searcher: jobSearcher, Materializer: materializer, Preparer: preparationGateway,
			DirectResolver: directResolver, RangePreparer: rangePreparer,
		}, nil
	}
	jobOperationTimeout := cfg.AcquisitionOperationTimeout
	acquisitionJobWorker, err := acquisitionjobservice.NewWorker(
		acquisitionJobRepository, jobProviderFactory, service,
		acquisitionjobservice.WorkerOptions{
			LeaseDuration: jobOperationTimeout + 30*time.Second, OperationTimeout: jobOperationTimeout,
			IdleInterval: time.Second, PreparingPollInterval: 10 * time.Second, RetryInterval: time.Minute,
			OnError: func(workerErr error) {
				logger.WarnContext(ctx, "background acquisition worker failed", "error", workerErr)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("configure background acquisition worker: %w", err)
	}
	var watchlistObserver *watchlistservice.Observer
	var watchlistWorker *watchlistservice.Worker
	var playbackAdvanceWorker *playbackadvanceservice.Worker
	var watchlistRepository *watchlistrepo.Repository
	if cfg.WatchlistEnabled {
		watchlistGateway, gatewayErr := plexwatchlist.New(plexwatchlist.Options{BaseURL: cfg.WatchlistBaseURL}, plexTokenSource, deps.httpClient)
		if gatewayErr != nil {
			return fmt.Errorf("configure Plex watchlist gateway: %w", gatewayErr)
		}
		openedRepository, repositoryErr := watchlistrepo.Open(ctx, cfg.DBPath, cfg.WatchlistAcquisitionEnabled)
		if repositoryErr != nil {
			return fmt.Errorf("open Plex watchlist queue: %w", repositoryErr)
		}
		watchlistRepository = openedRepository
		watchlistObserver, err = watchlistservice.NewObserver(watchlistGateway, watchlistRepository, watchlistservice.ObserverOptions{
			PollInterval: cfg.WatchlistPollInterval,
		})
		if err != nil {
			closeErr := watchlistRepository.Close()
			return errors.Join(fmt.Errorf("configure Plex watchlist observer: %w", err), closeErr)
		}
		watchlistWorker, err = watchlistservice.NewWorker(watchlistRepository, acquisitionJobManager, service, watchlistservice.WorkerOptions{
			LeaseDuration:     cfg.WatchlistLeaseDuration,
			OperationTimeout:  cfg.WatchlistAcquisitionTimeout,
			IdleInterval:      cfg.WatchlistWorkerIdleInterval,
			ReconcileInterval: cfg.WatchlistReconcileInterval,
			NotCachedCooldown: cfg.WatchlistNotCachedCooldown,
			RetryCooldown:     cfg.WatchlistRetryCooldown,
		})
		if err != nil {
			closeErr := watchlistRepository.Close()
			return errors.Join(fmt.Errorf("configure Plex watchlist acquisition worker: %w", err), closeErr)
		}
		if cfg.PlaybackAdvancementEnabled {
			playbackGateway, playbackErr := plexplayback.New(plexplayback.Options{
				BaseURL: cfg.PlexRefreshURL, LibraryRoot: "/blackpearl",
			}, plexTokenSource, deps.httpClient)
			if playbackErr != nil {
				closeErr := watchlistRepository.Close()
				return errors.Join(fmt.Errorf("configure Plex playback gateway: %w", playbackErr), closeErr)
			}
			metadataGateway, metadataErr := plexmetadata.New(cfg.PlaybackMetadataURL, plexTokenSource, deps.httpClient)
			if metadataErr != nil {
				closeErr := watchlistRepository.Close()
				return errors.Join(fmt.Errorf("configure Plex metadata gateway: %w", metadataErr), closeErr)
			}
			playbackAdvanceWorker, err = playbackadvanceservice.NewWorker(
				playbackGateway, service, watchlistRepository, metadataGateway,
				playbackadvanceservice.WorkerOptions{
					PollInterval: cfg.PlaybackPollInterval, OperationTimeout: cfg.PlaybackOperationTimeout,
					WatchlistPollInterval: cfg.WatchlistPollInterval,
					OnError: func(workerErr error) {
						logger.WarnContext(ctx, "playback advancement failed; retrying", "error", workerErr)
					},
				},
			)
			if err != nil {
				closeErr := watchlistRepository.Close()
				return errors.Join(fmt.Errorf("configure playback advancement worker: %w", err), closeErr)
			}
		}
	}
	startSetupRestore(ctx, service, logger, 2*time.Second)
	if watchlistObserver != nil || plexRefreshWorker != nil || acquisitionJobWorker != nil {
		backgroundContext, stopBackground := context.WithCancel(ctx)
		var backgroundGroup errgroup.Group
		if plexRefreshWorker != nil {
			backgroundGroup.Go(func() error {
				plexRefreshWorker.Run(backgroundContext)
				return nil
			})
		}
		if watchlistObserver != nil {
			backgroundGroup.Go(func() error {
				watchlistObserver.Run(backgroundContext)
				return nil
			})
		}
		if watchlistWorker != nil {
			backgroundGroup.Go(func() error {
				watchlistWorker.Run(backgroundContext)
				return nil
			})
		}
		if playbackAdvanceWorker != nil {
			backgroundGroup.Go(func() error {
				playbackAdvanceWorker.Run(backgroundContext)
				return nil
			})
		}
		backgroundGroup.Go(func() error {
			acquisitionJobWorker.Run(backgroundContext)
			return nil
		})
		defer func() {
			stopBackground()
			backgroundErr := backgroundGroup.Wait()
			if watchlistRepository != nil {
				backgroundErr = errors.Join(backgroundErr, watchlistRepository.Close())
			}
			runErr = errors.Join(runErr, backgroundErr)
		}()
	}
	var apiHandler http.Handler
	if watchlistObserver != nil {
		apiHandler, err = setuphandler.NewWithAcquisitionJobsAndWatchlist(service, acquisitionCoordinator, acquisitionJobManager, watchlistObserver, logger)
	} else {
		apiHandler, err = setuphandler.NewWithAcquisitionAndJobs(service, acquisitionCoordinator, acquisitionJobManager, logger)
	}
	if err != nil {
		return err
	}
	uiHandler, err := webui.Handler()
	if err != nil {
		return err
	}

	gate := &readinessGate{delegate: switcher}
	handler := otelhttp.NewHandler(httpserver.New(gate, logger, httpserver.Options{SetupAPI: apiHandler, UI: uiHandler}), "blackpearl.control")
	listener, err := deps.listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on setup address %s: %w", cfg.HTTPAddr, err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	gate.ready.Store(true)
	defer func() {
		gate.ready.Store(false)
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownContext); shutdownErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shut down setup HTTP: %w", shutdownErr))
		}
	}()
	logger.InfoContext(ctx, "BlackPearl browser setup available", "httpAddress", listener.Addr().String(), "filesystemEndpoint", nfs.Addr().String())

	select {
	case <-ctx.Done():
	case serveErr := <-serveErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			runErr = fmt.Errorf("serve setup HTTP: %w", serveErr)
		}
	}
	return runErr
}

type setupRestorer interface {
	Restore(ctx context.Context) error
}

func startSetupRestore(ctx context.Context, restorer setupRestorer, logger *slog.Logger, retryDelay time.Duration) {
	err := restorer.Restore(ctx)
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		return
	}
	logger.WarnContext(ctx, "saved browser setup could not be restored", "error", err)
	if !errors.Is(err, setupservice.ErrUnavailable) {
		return
	}
	go retrySetupRestore(ctx, restorer, logger, retryDelay)
}

func retrySetupRestore(ctx context.Context, restorer setupRestorer, logger *slog.Logger, retryDelay time.Duration) {
	delay := retryDelay
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		err := restorer.Restore(ctx)
		if err == nil {
			logger.InfoContext(ctx, "saved browser setup restored after retry")
			return
		}
		if !errors.Is(err, setupservice.ErrUnavailable) {
			if !errors.Is(err, domain.ErrNotFound) {
				logger.WarnContext(ctx, "saved browser setup retry stopped", "error", err)
			}
			return
		}
		logger.WarnContext(ctx, "saved browser setup retry failed", "error", err)
		if delay < time.Minute/2 {
			delay *= 2
		} else {
			delay = time.Minute
		}
	}
}

func resolveTorBoxToken(ctx context.Context, cfg config.Config) (_ string, resultErr error) {
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
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.New("close BLACKPEARL_TORBOX_API_TOKEN_FILE"))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", errors.New("read BLACKPEARL_TORBOX_API_TOKEN_FILE")
	}
	if len(content) > 4096 {
		return "", errors.New("BLACKPEARL_TORBOX_API_TOKEN_FILE exceeds 4096 bytes")
	}
	token := string(content)
	if strings.HasSuffix(token, "\r\n") {
		token = strings.TrimSuffix(token, "\r\n")
	} else {
		token = strings.TrimSuffix(token, "\n")
	}
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
