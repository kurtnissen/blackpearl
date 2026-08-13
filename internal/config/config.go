// Package config parses and validates BlackPearl process configuration.
package config

import (
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
	DataDir         string             `env:"BLACKPEARL_DATA_DIR" envDefault:"/var/lib/blackpearl"`
	DBPath          string             `env:"BLACKPEARL_DB_PATH" envDefault:"/var/lib/blackpearl/blackpearl.db"`
	CacheDir        string             `env:"BLACKPEARL_CACHE_DIR" envDefault:"/var/lib/blackpearl/cache"`
	MountPath       string             `env:"BLACKPEARL_MOUNT_PATH" envDefault:"/mnt/blackpearl"`
	POCSource       string             `env:"BLACKPEARL_POC_SOURCE"`
	HTTPAddr        string             `env:"BLACKPEARL_HTTP_ADDR" envDefault:":8080"`
	LogLevel        string             `env:"BLACKPEARL_LOG_LEVEL" envDefault:"info"`
	StorageMode     domain.StorageMode `env:"BLACKPEARL_STORAGE_MODE" envDefault:"persistent"`
	CacheMaxBytes   int64              `env:"BLACKPEARL_CACHE_MAX_BYTES" envDefault:"0"`
	CacheChunkBytes int64              `env:"BLACKPEARL_CACHE_CHUNK_BYTES" envDefault:"262144"`
	RangeOriginURL  string             `env:"BLACKPEARL_RANGE_ORIGIN_URL"`
	RangeObjectID   string             `env:"BLACKPEARL_RANGE_OBJECT_ID"`
	RangeTimeout    time.Duration      `env:"BLACKPEARL_RANGE_TIMEOUT" envDefault:"30s"`
	FilesystemMode  string             `env:"BLACKPEARL_FILESYSTEM_MODE" envDefault:"fuse"`
	NFSAddr         string             `env:"BLACKPEARL_NFS_ADDR" envDefault:":2049"`
	Plex            Plex
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
	switch c.StorageMode {
	case domain.StorageModePersistent:
		if c.CacheMaxBytes < 0 {
			return errors.New("BLACKPEARL_CACHE_MAX_BYTES must not be negative")
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
		if c.RangeTimeout <= 0 {
			return errors.New("BLACKPEARL_RANGE_TIMEOUT must be positive in rolling mode")
		}
		if c.POCSource != "" {
			return errors.New("BLACKPEARL_POC_SOURCE must be empty in rolling mode")
		}
		if strings.TrimSpace(c.RangeObjectID) == "" || strings.ContainsRune(c.RangeObjectID, 0) {
			return errors.New("BLACKPEARL_RANGE_OBJECT_ID is required and must not contain NUL in rolling mode")
		}
		origin, err := url.Parse(c.RangeOriginURL)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" {
			return fmt.Errorf("BLACKPEARL_RANGE_ORIGIN_URL must be an absolute HTTP URL in rolling mode: %q", c.RangeOriginURL)
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
