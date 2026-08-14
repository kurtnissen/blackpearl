package domain

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

var setupObjectIDPattern = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)

// MaximumSetupManifestItems bounds setup work, API payloads, and Plex scan fan-out.
const MaximumSetupManifestItems = 100

// MediaCandidate is public metadata for one range-readable provider object.
type MediaCandidate struct {
	ObjectID  string `json:"objectId"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Size      int64  `json:"size"`
}

// SetupConfiguration is the non-secret persisted selection shown to Plex.
type SetupConfiguration struct {
	ObjectID  string `json:"objectId"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Size      int64  `json:"size"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
}

// SetupManifest is one validated, atomically published Plex library snapshot.
type SetupManifest struct {
	Items []SetupConfiguration `json:"items"`
}

// NewMediaCandidate validates normalized provider metadata for the setup UI.
func NewMediaCandidate(objectID string, name string, size int64) (MediaCandidate, error) {
	if !setupObjectIDPattern.MatchString(objectID) {
		return MediaCandidate{}, errors.New("media candidate object ID must use canonical positive torrent:file form")
	}
	cleanName := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if cleanName == "" || strings.ContainsRune(cleanName, 0) || path.IsAbs(cleanName) || path.Clean(cleanName) != cleanName {
		return MediaCandidate{}, errors.New("media candidate name must be a safe relative path")
	}
	for _, segment := range strings.Split(cleanName, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return MediaCandidate{}, errors.New("media candidate name contains an unsafe path segment")
		}
	}
	extension := strings.ToLower(path.Ext(cleanName))
	if extension != ".mp4" && extension != ".mkv" {
		return MediaCandidate{}, fmt.Errorf("media candidate extension is unsupported: %q", extension)
	}
	if size <= 0 {
		return MediaCandidate{}, fmt.Errorf("media candidate size must be positive: %d", size)
	}
	return MediaCandidate{ObjectID: objectID, Name: cleanName, Extension: extension, Size: size}, nil
}

// NewSetupConfiguration validates one Plex-visible selection.
func NewSetupConfiguration(candidate MediaCandidate, title string, year int) (SetupConfiguration, error) {
	validated, err := NewMediaCandidate(candidate.ObjectID, candidate.Name, candidate.Size)
	if err != nil {
		return SetupConfiguration{}, fmt.Errorf("validate media candidate: %w", err)
	}
	if candidate.Extension != "" && strings.ToLower(candidate.Extension) != validated.Extension {
		return SetupConfiguration{}, errors.New("media candidate extension does not match its name")
	}
	cleanTitle := strings.TrimSpace(title)
	if err := validateTitle(cleanTitle); err != nil {
		return SetupConfiguration{}, err
	}
	if year < 1888 || year > 2100 {
		return SetupConfiguration{}, fmt.Errorf("year must be between 1888 and 2100: %d", year)
	}
	return SetupConfiguration{
		ObjectID:  validated.ObjectID,
		Name:      validated.Name,
		Extension: validated.Extension,
		Size:      validated.Size,
		Title:     cleanTitle,
		Year:      year,
	}, nil
}

// Candidate returns the provider metadata embedded in the selection.
func (c SetupConfiguration) Candidate() MediaCandidate {
	return MediaCandidate{ObjectID: c.ObjectID, Name: c.Name, Extension: c.Extension, Size: c.Size}
}

// NewSetupManifest validates and copies one bounded movie manifest.
func NewSetupManifest(items []SetupConfiguration) (SetupManifest, error) {
	if len(items) == 0 {
		return SetupManifest{}, errors.New("setup manifest requires at least one item")
	}
	if len(items) > MaximumSetupManifestItems {
		return SetupManifest{}, fmt.Errorf("setup manifest supports at most %d items", MaximumSetupManifestItems)
	}
	validated := make([]SetupConfiguration, 0, len(items))
	objectIDs := make(map[string]struct{}, len(items))
	virtualPaths := make(map[string]struct{}, len(items))
	for index := range items {
		item, err := NewSetupConfiguration(items[index].Candidate(), items[index].Title, items[index].Year)
		if err != nil {
			return SetupManifest{}, fmt.Errorf("validate setup manifest item %d: %w", index, err)
		}
		if _, exists := objectIDs[item.ObjectID]; exists {
			return SetupManifest{}, fmt.Errorf("setup manifest contains duplicate object ID at item %d", index)
		}
		virtualPath := setupVirtualPath(item)
		if _, exists := virtualPaths[virtualPath]; exists {
			return SetupManifest{}, fmt.Errorf("setup manifest contains duplicate Plex path at item %d", index)
		}
		objectIDs[item.ObjectID] = struct{}{}
		virtualPaths[virtualPath] = struct{}{}
		validated = append(validated, item)
	}
	return SetupManifest{Items: validated}, nil
}

func setupVirtualPath(configuration SetupConfiguration) string {
	displayName := fmt.Sprintf("%s (%d)", configuration.Title, configuration.Year)
	return path.Join("Movies", displayName, displayName+configuration.Extension)
}
