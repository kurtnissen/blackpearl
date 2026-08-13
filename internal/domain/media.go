// Package domain contains BlackPearl's infrastructure-independent types.
package domain

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	// ErrNotFound indicates that a requested domain object does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotConfigured indicates that an optional capability has no implementation configured.
	ErrNotConfigured = errors.New("not configured")
)

// MediaID uniquely identifies a catalog item.
type MediaID string

// MediaType identifies the Plex-compatible catalog hierarchy for an item.
type MediaType string

const (
	// MediaTypeMovie represents a movie catalog item.
	MediaTypeMovie MediaType = "movie"
)

// Media describes an immutable item exposed by PearlFS.
type Media struct {
	ID          MediaID
	Type        MediaType
	Title       string
	Year        int
	Extension   string
	VirtualPath string
	Size        int64
	CacheKey    string
}

// NewMovie validates movie metadata and derives its Plex-compatible virtual path.
func NewMovie(id MediaID, title string, year int, extension string, size int64, cacheKey string) (Media, error) {
	if id == "" {
		return Media{}, errors.New("media id is required")
	}
	if err := validatePathSegment("title", title); err != nil {
		return Media{}, err
	}
	if year < 1 || year > 9999 {
		return Media{}, fmt.Errorf("year must be between 1 and 9999: %d", year)
	}
	if len(extension) < 2 || extension[0] != '.' {
		return Media{}, fmt.Errorf("extension must begin with a dot: %q", extension)
	}
	if err := validatePathSegment("extension", extension[1:]); err != nil {
		return Media{}, err
	}
	if size < 0 {
		return Media{}, fmt.Errorf("size must not be negative: %d", size)
	}
	if strings.TrimSpace(cacheKey) == "" {
		return Media{}, errors.New("cache key is required")
	}

	displayName := fmt.Sprintf("%s (%d)", title, year)
	return Media{
		ID:          id,
		Type:        MediaTypeMovie,
		Title:       title,
		Year:        year,
		Extension:   extension,
		VirtualPath: path.Join("Movies", displayName, displayName+extension),
		Size:        size,
		CacheKey:    cacheKey,
	}, nil
}

func validatePathSegment(name string, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return fmt.Errorf("%s must be a non-empty path segment", name)
	}
	if strings.ContainsAny(value, "/\\") || strings.ContainsRune(value, 0) || strings.Contains(value, "..") {
		return fmt.Errorf("%s contains unsafe path characters: %q", name, value)
	}
	return nil
}
