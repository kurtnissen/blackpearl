// Package config parses and validates BlackPearl process configuration.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/caarlos0/env/v11"
)

// Plex contains optional settings for refreshing one Plex library.
type Plex struct {
	URL       string `env:"BLACKPEARL_PLEX_URL"`
	Token     string `env:"BLACKPEARL_PLEX_TOKEN"`
	SectionID string `env:"BLACKPEARL_PLEX_SECTION_ID"`
}

// Enabled reports whether the complete optional Plex integration is configured.
func (p Plex) Enabled() bool {
	return p.URL != "" && p.Token != "" && p.SectionID != ""
}

// Config contains all process configuration.
type Config struct {
	DataDir                     string             `env:"BLACKPEARL_DATA_DIR" envDefault:"/var/lib/blackpearl"`
	DBPath                      string             `env:"BLACKPEARL_DB_PATH" envDefault:"/var/lib/blackpearl/blackpearl.db"`
	CacheDir                    string             `env:"BLACKPEARL_CACHE_DIR" envDefault:"/var/lib/blackpearl/cache"`
	MountPath                   string             `env:"BLACKPEARL_MOUNT_PATH" envDefault:"/mnt/blackpearl"`
	POCSource                   string             `env:"BLACKPEARL_POC_SOURCE"`
	HTTPAddr                    string             `env:"BLACKPEARL_HTTP_ADDR" envDefault:":8080"`
	LogLevel                    string             `env:"BLACKPEARL_LOG_LEVEL" envDefault:"info"`
	StorageMode                 domain.StorageMode `env:"BLACKPEARL_STORAGE_MODE" envDefault:"persistent"`
	CacheMaxBytes               int64              `env:"BLACKPEARL_CACHE_MAX_BYTES" envDefault:"0"`
	CacheChunkBytes             int64              `env:"BLACKPEARL_CACHE_CHUNK_BYTES" envDefault:"262144"`
	CacheReadAheadChunks        int                `env:"BLACKPEARL_CACHE_READ_AHEAD_CHUNKS" envDefault:"0"`
	CacheNextEpisodeChunks      int                `env:"BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS" envDefault:"0"`
	RangeProvider               string             `env:"BLACKPEARL_RANGE_PROVIDER" envDefault:"http-range"`
	RangeOriginURL              string             `env:"BLACKPEARL_RANGE_ORIGIN_URL"`
	RangeObjectID               string             `env:"BLACKPEARL_RANGE_OBJECT_ID"`
	RangeTimeout                time.Duration      `env:"BLACKPEARL_RANGE_TIMEOUT" envDefault:"30s"`
	AcquisitionOperationTimeout time.Duration      `env:"BLACKPEARL_ACQUISITION_OPERATION_TIMEOUT" envDefault:"2m"`
	TorBoxAPIURL                string             `env:"BLACKPEARL_TORBOX_API_URL" envDefault:"https://api.torbox.app/v1/api/"`
	TorBoxAPIToken              string             `env:"BLACKPEARL_TORBOX_API_TOKEN"`
	TorBoxAPITokenFile          string             `env:"BLACKPEARL_TORBOX_API_TOKEN_FILE"`
	SetupEnabled                bool               `env:"BLACKPEARL_SETUP_ENABLED" envDefault:"false"`
	SetupDir                    string             `env:"BLACKPEARL_SETUP_DIR" envDefault:"/var/lib/blackpearl/setup"`
	SetupBootstrapToken         string             `env:"BLACKPEARL_SETUP_BOOTSTRAP_TOKEN"`
	WatchlistEnabled            bool               `env:"BLACKPEARL_WATCHLIST_ENABLED" envDefault:"false"`
	WatchlistBaseURL            string             `env:"BLACKPEARL_WATCHLIST_BASE_URL" envDefault:"https://discover.provider.plex.tv"`
	WatchlistPollInterval       time.Duration      `env:"BLACKPEARL_WATCHLIST_POLL_INTERVAL" envDefault:"15m"`
	WatchlistPreferencesPath    string             `env:"BLACKPEARL_WATCHLIST_PREFERENCES_PATH"`
	WatchlistTokenFile          string             `env:"BLACKPEARL_WATCHLIST_TOKEN_FILE"`
	WatchlistAcquisitionEnabled bool               `env:"BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED" envDefault:"false"`
	WatchlistLeaseDuration      time.Duration      `env:"BLACKPEARL_WATCHLIST_LEASE_DURATION" envDefault:"10m"`
	WatchlistAcquisitionTimeout time.Duration      `env:"BLACKPEARL_WATCHLIST_ACQUISITION_TIMEOUT" envDefault:"5m"`
	WatchlistWorkerIdleInterval time.Duration      `env:"BLACKPEARL_WATCHLIST_WORKER_IDLE_INTERVAL" envDefault:"30s"`
	WatchlistNotCachedCooldown  time.Duration      `env:"BLACKPEARL_WATCHLIST_NOT_CACHED_COOLDOWN" envDefault:"6h"`
	WatchlistRetryCooldown      time.Duration      `env:"BLACKPEARL_WATCHLIST_RETRY_COOLDOWN" envDefault:"15m"`
	PlexRefreshEnabled          bool               `env:"BLACKPEARL_PLEX_REFRESH_ENABLED" envDefault:"false"`
	PlexRefreshURL              string             `env:"BLACKPEARL_PLEX_REFRESH_URL"`
	FilesystemMode              string             `env:"BLACKPEARL_FILESYSTEM_MODE" envDefault:"fuse"`
	NFSAddr                     string             `env:"BLACKPEARL_NFS_ADDR" envDefault:":2049"`
	Plex                        Plex
}

// Load parses configuration from the current process environment.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Parse parses configuration from an explicit environment map for deterministic callers and tests.
func Parse(environment map[string]string) (Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environment}); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	switch c.FilesystemMode {
	case "fuse":
	case "nfs":
		_, portValue, err := net.SplitHostPort(c.NFSAddr)
		if err != nil {
			return fmt.Errorf("BLACKPEARL_NFS_ADDR must be a TCP listen address: %q", c.NFSAddr)
		}
		port, err := strconv.Atoi(portValue)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("BLACKPEARL_NFS_ADDR must contain a numeric port from 1 to 65535: %q", c.NFSAddr)
		}
	default:
		return fmt.Errorf("BLACKPEARL_FILESYSTEM_MODE must be fuse or nfs: %q", c.FilesystemMode)
	}
	if c.SetupEnabled && ((c.StorageMode != domain.StorageModeRolling && c.StorageMode != domain.StorageModePersistent) || c.RangeProvider != "torbox-torrent" || c.FilesystemMode != "nfs") {
		return errors.New("BLACKPEARL_SETUP_ENABLED requires persistent or rolling storage, torbox-torrent provider, and nfs filesystem mode")
	}
	if c.SetupEnabled {
		decoded, err := hex.DecodeString(c.SetupBootstrapToken)
		if err != nil || len(decoded) != 32 || c.SetupBootstrapToken != strings.ToLower(c.SetupBootstrapToken) {
			return errors.New("BLACKPEARL_SETUP_BOOTSTRAP_TOKEN must be exactly 64 lowercase hexadecimal characters")
		}
		if c.AcquisitionOperationTimeout < 10*time.Second || c.AcquisitionOperationTimeout > 10*time.Minute {
			return errors.New("BLACKPEARL_ACQUISITION_OPERATION_TIMEOUT must be between 10s and 10m")
		}
	} else if c.SetupBootstrapToken != "" {
		return errors.New("BLACKPEARL_SETUP_BOOTSTRAP_TOKEN requires BLACKPEARL_SETUP_ENABLED=true")
	}
	if c.WatchlistEnabled {
		if !c.SetupEnabled {
			return errors.New("BLACKPEARL_WATCHLIST_ENABLED requires BLACKPEARL_SETUP_ENABLED=true")
		}
		sources := 0
		for _, value := range []string{c.WatchlistPreferencesPath, c.WatchlistTokenFile} {
			if value != "" {
				sources++
			}
		}
		if sources != 1 {
			return errors.New("exactly one of BLACKPEARL_WATCHLIST_PREFERENCES_PATH or BLACKPEARL_WATCHLIST_TOKEN_FILE is required")
		}
		if c.WatchlistPollInterval < time.Minute || c.WatchlistPollInterval > 24*time.Hour {
			return errors.New("BLACKPEARL_WATCHLIST_POLL_INTERVAL must be between 1m and 24h")
		}
		watchlistURL, err := url.Parse(c.WatchlistBaseURL)
		if err != nil || watchlistURL.Scheme != "https" || watchlistURL.Host == "" || watchlistURL.User != nil || watchlistURL.RawQuery != "" || watchlistURL.Fragment != "" {
			return errors.New("BLACKPEARL_WATCHLIST_BASE_URL must be an absolute HTTPS URL without credentials, query, or fragment")
		}
	} else if c.WatchlistPreferencesPath != "" || c.WatchlistTokenFile != "" {
		return errors.New("BLACKPEARL_WATCHLIST_PREFERENCES_PATH and BLACKPEARL_WATCHLIST_TOKEN_FILE require BLACKPEARL_WATCHLIST_ENABLED=true")
	}
	if c.WatchlistAcquisitionEnabled {
		if !c.WatchlistEnabled {
			return errors.New("BLACKPEARL_WATCHLIST_ACQUISITION_ENABLED requires BLACKPEARL_WATCHLIST_ENABLED=true")
		}
		if c.WatchlistAcquisitionTimeout < 10*time.Second || c.WatchlistAcquisitionTimeout > 30*time.Minute {
			return errors.New("BLACKPEARL_WATCHLIST_ACQUISITION_TIMEOUT must be between 10s and 30m")
		}
		if c.WatchlistLeaseDuration < c.WatchlistAcquisitionTimeout+time.Minute || c.WatchlistLeaseDuration > time.Hour {
			return errors.New("BLACKPEARL_WATCHLIST_LEASE_DURATION must exceed the acquisition timeout by at least 1m and not exceed 1h")
		}
		if c.WatchlistWorkerIdleInterval < time.Second || c.WatchlistWorkerIdleInterval > 10*time.Minute {
			return errors.New("BLACKPEARL_WATCHLIST_WORKER_IDLE_INTERVAL must be between 1s and 10m")
		}
		if c.WatchlistNotCachedCooldown < time.Minute || c.WatchlistNotCachedCooldown > 7*24*time.Hour {
			return errors.New("BLACKPEARL_WATCHLIST_NOT_CACHED_COOLDOWN must be between 1m and 168h")
		}
		if c.WatchlistRetryCooldown < time.Minute || c.WatchlistRetryCooldown > 24*time.Hour {
			return errors.New("BLACKPEARL_WATCHLIST_RETRY_COOLDOWN must be between 1m and 24h")
		}
	}
	if c.PlexRefreshEnabled {
		if !c.WatchlistEnabled {
			return errors.New("BLACKPEARL_PLEX_REFRESH_ENABLED requires BLACKPEARL_WATCHLIST_ENABLED for its read-only credential source")
		}
		plexURL, err := url.Parse(c.PlexRefreshURL)
		if err != nil || (plexURL.Scheme != "http" && plexURL.Scheme != "https") || plexURL.Host == "" || plexURL.User != nil || plexURL.RawQuery != "" || plexURL.Fragment != "" {
			return errors.New("BLACKPEARL_PLEX_REFRESH_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
		}
	} else if c.PlexRefreshURL != "" {
		return errors.New("BLACKPEARL_PLEX_REFRESH_URL requires BLACKPEARL_PLEX_REFRESH_ENABLED=true")
	}
	switch c.StorageMode {
	case domain.StorageModePersistent:
		if c.SetupEnabled {
			if err := c.validatePersistentBrowserCache(); err != nil {
				return err
			}
			break
		}
		if c.CacheMaxBytes < 0 {
			return errors.New("BLACKPEARL_CACHE_MAX_BYTES must not be negative")
		}
		if c.CacheReadAheadChunks != 0 {
			return errors.New("BLACKPEARL_CACHE_READ_AHEAD_CHUNKS requires rolling mode")
		}
		if c.CacheNextEpisodeChunks != 0 {
			return errors.New("BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS requires rolling mode")
		}
		if c.TorBoxAPIToken != "" || c.TorBoxAPITokenFile != "" || c.RangeProvider != "http-range" {
			return errors.New("BLACKPEARL_TORBOX_API_TOKEN and non-default BLACKPEARL_RANGE_PROVIDER require rolling mode")
		}
		if c.RangeOriginURL != "" || c.RangeObjectID != "" {
			return errors.New("BLACKPEARL_RANGE_ORIGIN_URL and BLACKPEARL_RANGE_OBJECT_ID require rolling mode")
		}
	case domain.StorageModeRolling:
		if c.CacheMaxBytes <= 0 {
			return errors.New("BLACKPEARL_CACHE_MAX_BYTES must be positive in rolling mode")
		}
		if c.CacheChunkBytes <= 0 || c.CacheChunkBytes > c.CacheMaxBytes {
			return errors.New("BLACKPEARL_CACHE_CHUNK_BYTES must be positive and no larger than BLACKPEARL_CACHE_MAX_BYTES in rolling mode")
		}
		if c.CacheReadAheadChunks < 0 || c.CacheReadAheadChunks > 64 {
			return errors.New("BLACKPEARL_CACHE_READ_AHEAD_CHUNKS must be between 0 and 64 in rolling mode")
		}
		if c.CacheNextEpisodeChunks < 0 || c.CacheNextEpisodeChunks > 256 {
			return errors.New("BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS must be between 0 and 256 in rolling mode")
		}
		if c.RangeTimeout <= 0 {
			return errors.New("BLACKPEARL_RANGE_TIMEOUT must be positive in rolling mode")
		}
		if c.POCSource != "" {
			return errors.New("BLACKPEARL_POC_SOURCE must be empty in rolling mode")
		}
		if !c.SetupEnabled && (strings.TrimSpace(c.RangeObjectID) == "" || strings.ContainsRune(c.RangeObjectID, 0)) {
			return errors.New("BLACKPEARL_RANGE_OBJECT_ID is required and must not contain NUL in rolling mode")
		}
		switch c.RangeProvider {
		case "http-range":
			if c.TorBoxAPIToken != "" || c.TorBoxAPITokenFile != "" {
				return errors.New("BLACKPEARL_TORBOX_API_TOKEN and BLACKPEARL_TORBOX_API_TOKEN_FILE require BLACKPEARL_RANGE_PROVIDER=torbox-torrent")
			}
			origin, err := url.Parse(c.RangeOriginURL)
			if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" {
				return fmt.Errorf("BLACKPEARL_RANGE_ORIGIN_URL must be an absolute HTTP URL in HTTP rolling mode: %q", c.RangeOriginURL)
			}
		case "torbox-torrent":
			if c.RangeOriginURL != "" {
				return errors.New("BLACKPEARL_RANGE_ORIGIN_URL must be empty with BLACKPEARL_RANGE_PROVIDER=torbox-torrent")
			}
			hasInlineToken := c.TorBoxAPIToken != ""
			hasTokenFile := c.TorBoxAPITokenFile != ""
			if c.SetupEnabled {
				if hasInlineToken || hasTokenFile || c.RangeObjectID != "" {
					return errors.New("browser setup mode requires TorBox token and object ID to be absent from environment")
				}
			} else if hasInlineToken == hasTokenFile {
				return errors.New("exactly one of BLACKPEARL_TORBOX_API_TOKEN or BLACKPEARL_TORBOX_API_TOKEN_FILE is required")
			}
			if hasInlineToken && (strings.TrimSpace(c.TorBoxAPIToken) == "" || strings.TrimSpace(c.TorBoxAPIToken) != c.TorBoxAPIToken) {
				return errors.New("BLACKPEARL_TORBOX_API_TOKEN is required without surrounding whitespace")
			}
			if hasTokenFile && !filepath.IsAbs(c.TorBoxAPITokenFile) {
				return errors.New("BLACKPEARL_TORBOX_API_TOKEN_FILE must be an absolute path")
			}
			torboxURL, err := url.Parse(c.TorBoxAPIURL)
			if err != nil || torboxURL.Scheme != "https" || torboxURL.Host == "" || torboxURL.User != nil || torboxURL.RawQuery != "" || torboxURL.Fragment != "" {
				return errors.New("BLACKPEARL_TORBOX_API_URL must be an absolute HTTPS URL without credentials, query, or fragment")
			}
			if !c.SetupEnabled && !canonicalTorBoxObjectID(c.RangeObjectID) {
				return errors.New("BLACKPEARL_RANGE_OBJECT_ID must use canonical positive <torrent-id>:<file-id> form for TorBox")
			}
		default:
			return fmt.Errorf("BLACKPEARL_RANGE_PROVIDER must be http-range or torbox-torrent: %q", c.RangeProvider)
		}
	default:
		return fmt.Errorf("BLACKPEARL_STORAGE_MODE must be persistent or rolling: %q", c.StorageMode)
	}
	paths := []struct {
		variable string
		value    string
		optional bool
	}{
		{variable: "BLACKPEARL_DATA_DIR", value: c.DataDir},
		{variable: "BLACKPEARL_DB_PATH", value: c.DBPath},
		{variable: "BLACKPEARL_CACHE_DIR", value: c.CacheDir},
		{variable: "BLACKPEARL_MOUNT_PATH", value: c.MountPath},
		{variable: "BLACKPEARL_POC_SOURCE", value: c.POCSource, optional: true},
		{variable: "BLACKPEARL_SETUP_DIR", value: c.SetupDir},
		{variable: "BLACKPEARL_WATCHLIST_PREFERENCES_PATH", value: c.WatchlistPreferencesPath, optional: true},
		{variable: "BLACKPEARL_WATCHLIST_TOKEN_FILE", value: c.WatchlistTokenFile, optional: true},
	}
	for _, configuredPath := range paths {
		if configuredPath.optional && configuredPath.value == "" {
			continue
		}
		if !filepath.IsAbs(configuredPath.value) {
			return fmt.Errorf("%s must be an absolute path: %q", configuredPath.variable, configuredPath.value)
		}
	}

	plexValues := 0
	for _, value := range []string{c.Plex.URL, c.Plex.Token, c.Plex.SectionID} {
		if value != "" {
			plexValues++
		}
	}
	if plexValues != 0 && plexValues != 3 {
		return errors.New("Plex configuration requires BLACKPEARL_PLEX_URL, BLACKPEARL_PLEX_TOKEN, and BLACKPEARL_PLEX_SECTION_ID together")
	}
	if c.Plex.Enabled() {
		parsed, err := url.Parse(c.Plex.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("BLACKPEARL_PLEX_URL must be an absolute HTTP URL: %q", c.Plex.URL)
		}
	}
	return nil
}

func (c Config) validatePersistentBrowserCache() error {
	if c.CacheMaxBytes != 0 {
		return errors.New("BLACKPEARL_CACHE_MAX_BYTES must be zero in persistent browser setup mode")
	}
	if c.CacheChunkBytes <= 0 {
		return errors.New("BLACKPEARL_CACHE_CHUNK_BYTES must be positive in persistent browser setup mode")
	}
	if c.CacheReadAheadChunks < 0 || c.CacheReadAheadChunks > 64 {
		return errors.New("BLACKPEARL_CACHE_READ_AHEAD_CHUNKS must be between 0 and 64 in persistent browser setup mode")
	}
	if c.CacheNextEpisodeChunks < 0 || c.CacheNextEpisodeChunks > 256 {
		return errors.New("BLACKPEARL_CACHE_NEXT_EPISODE_CHUNKS must be between 0 and 256 in persistent browser setup mode")
	}
	if c.RangeTimeout <= 0 {
		return errors.New("BLACKPEARL_RANGE_TIMEOUT must be positive in persistent browser setup mode")
	}
	if c.POCSource != "" {
		return errors.New("BLACKPEARL_POC_SOURCE must be empty in persistent browser setup mode")
	}
	if c.RangeOriginURL != "" {
		return errors.New("BLACKPEARL_RANGE_ORIGIN_URL must be empty with BLACKPEARL_RANGE_PROVIDER=torbox-torrent")
	}
	if c.TorBoxAPIToken != "" || c.TorBoxAPITokenFile != "" || c.RangeObjectID != "" {
		return errors.New("browser setup mode requires TorBox token and object ID to be absent from environment")
	}
	torboxURL, err := url.Parse(c.TorBoxAPIURL)
	if err != nil || torboxURL.Scheme != "https" || torboxURL.Host == "" || torboxURL.User != nil || torboxURL.RawQuery != "" || torboxURL.Fragment != "" {
		return errors.New("BLACKPEARL_TORBOX_API_URL must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	return nil
}

func canonicalTorBoxObjectID(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part[0] == '0' {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		if _, err := strconv.ParseInt(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}
