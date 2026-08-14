// Package domain contains BlackPearl's infrastructure-independent types.
package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var (
	// ErrNotFound indicates that a requested domain object does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotConfigured indicates that an optional capability has no implementation configured.
	ErrNotConfigured = errors.New("not configured")
	// ErrUnauthorized indicates that an external provider rejected credentials.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrCleanupDeferred indicates that primary state committed durably but
	// removal of inactive private state must be retried later.
	ErrCleanupDeferred = errors.New("cleanup deferred")
)

// MediaID uniquely identifies a catalog item.
type MediaID string

// MediaType identifies the Plex-compatible catalog hierarchy for an item.
type MediaType string

// StorageMode selects a PearlCache retention strategy.
type StorageMode string

const (
	maximumPlexTitleBytes = 200
	// MediaTypeMovie represents a movie catalog item.
	MediaTypeMovie MediaType = "movie"
	// MediaTypeEpisode represents one episode in a Plex TV hierarchy.
	MediaTypeEpisode MediaType = "episode"
	// StorageModePersistent retains complete acquired objects when capacity permits.
	StorageModePersistent StorageMode = "persistent"
	// StorageModeRolling retains a byte-bounded rolling set of object ranges.
	StorageModeRolling StorageMode = "rolling"
)

// BackingRef identifies an object without exposing provider-specific types or local paths.
type BackingRef struct {
	Provider string
	ObjectID string
}

// NewBackingRef validates a provider-neutral object reference.
func NewBackingRef(provider string, objectID string) (BackingRef, error) {
	if err := validateProvider(provider); err != nil {
		return BackingRef{}, err
	}
	if strings.TrimSpace(objectID) == "" || strings.ContainsRune(objectID, 0) {
		return BackingRef{}, errors.New("backing object ID is required and must not contain NUL")
	}
	return BackingRef{Provider: provider, ObjectID: objectID}, nil
}

// Media describes an immutable item exposed by PearlFS.
type Media struct {
	ID          MediaID
	Type        MediaType
	Title       string
	Year        int
	Extension   string
	VirtualPath string
	Size        int64
	Backing     BackingRef
}

// ReadHandle is a context-aware, sized, random-access logical media object.
//
// Implementations may fetch a range from local persistent storage, a rolling
// cache, or an authorized backing provider. A complete local file is not
// required.
type ReadHandle interface {
	ReadAt(ctx context.Context, destination []byte, offset int64) (int, error)
	io.Closer
	Size() int64
}

// NewMovie validates movie metadata and derives its Plex-compatible virtual path.
func NewMovie(id MediaID, title string, year int, extension string, size int64, backing BackingRef) (Media, error) {
	if id == "" {
		return Media{}, errors.New("media id is required")
	}
	if err := validateTitle(title); err != nil {
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
	if _, err := NewBackingRef(backing.Provider, backing.ObjectID); err != nil {
		return Media{}, fmt.Errorf("invalid backing reference: %w", err)
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
		Backing:     backing,
	}, nil
}

// NewEpisode validates episode metadata and derives its Plex-compatible path.
func NewEpisode(id MediaID, showTitle string, year int, season int, episode int, episodeTitle string, extension string, size int64, backing BackingRef) (Media, error) {
	if id == "" {
		return Media{}, errors.New("media id is required")
	}
	if err := validateTitle(showTitle); err != nil {
		return Media{}, fmt.Errorf("invalid show title: %w", err)
	}
	if err := validateTitle(episodeTitle); err != nil {
		return Media{}, fmt.Errorf("invalid episode title: %w", err)
	}
	if year < 1 || year > 9999 {
		return Media{}, fmt.Errorf("year must be between 1 and 9999: %d", year)
	}
	if season < 0 || season > 99 {
		return Media{}, fmt.Errorf("season must be between 0 and 99: %d", season)
	}
	if episode < 1 || episode > 999 {
		return Media{}, fmt.Errorf("episode must be between 1 and 999: %d", episode)
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
	if _, err := NewBackingRef(backing.Provider, backing.ObjectID); err != nil {
		return Media{}, fmt.Errorf("invalid backing reference: %w", err)
	}

	showDisplayName := fmt.Sprintf("%s (%d)", showTitle, year)
	seasonName := fmt.Sprintf("Season %02d", season)
	filename := fmt.Sprintf("%s - S%02dE%02d - %s%s", showDisplayName, season, episode, episodeTitle, extension)
	if len(filename) > 255 {
		return Media{}, errors.New("episode filename must not exceed 255 bytes")
	}
	return Media{
		ID:          id,
		Type:        MediaTypeEpisode,
		Title:       episodeTitle,
		Year:        year,
		Extension:   extension,
		VirtualPath: path.Join("TV Shows", showDisplayName, seasonName, filename),
		Size:        size,
		Backing:     backing,
	}, nil
}

func validateTitle(title string) error {
	if err := validatePathSegment("title", title); err != nil {
		return err
	}
	if len(title) > maximumPlexTitleBytes {
		return fmt.Errorf("title must not exceed %d bytes", maximumPlexTitleBytes)
	}
	return nil
}

func validateProvider(provider string) error {
	if provider == "" {
		return errors.New("backing provider is required")
	}
	for index, character := range provider {
		valid := character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			(index > 0 && (character == '.' || character == '_' || character == '-'))
		if !valid {
			return fmt.Errorf("backing provider contains invalid character %q", character)
		}
	}
	return nil
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
