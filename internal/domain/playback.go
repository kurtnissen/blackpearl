package domain

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	maximumPlaybackExternalIDBytes = 512
	maximumPlaybackPathBytes       = 4 << 10
	maximumPlaybackDuration        = 7 * 24 * time.Hour
)

// PlaybackState is one normalized active Plex player state.
type PlaybackState string

const (
	// PlaybackStatePlaying means the episode is actively advancing.
	PlaybackStatePlaying PlaybackState = "playing"
	// PlaybackStatePaused means the episode remains in an active paused session.
	PlaybackStatePaused PlaybackState = "paused"
)

// EpisodeCoordinate identifies one exact, non-special TV episode.
type EpisodeCoordinate struct {
	season  int
	episode int
}

// NewEpisodeCoordinate validates one exact episode coordinate.
func NewEpisodeCoordinate(season int, episode int) (EpisodeCoordinate, error) {
	if season < 1 || season > 99 {
		return EpisodeCoordinate{}, fmt.Errorf("episode season must be between 1 and 99: %d", season)
	}
	if episode < 1 || episode > 999 {
		return EpisodeCoordinate{}, fmt.Errorf("episode number must be between 1 and 999: %d", episode)
	}
	return EpisodeCoordinate{season: season, episode: episode}, nil
}

// Season returns the one-based season number.
func (c EpisodeCoordinate) Season() int { return c.season }

// Episode returns the one-based episode number.
func (c EpisodeCoordinate) Episode() int { return c.episode }

// After reports whether this coordinate is strictly later in canonical order.
func (c EpisodeCoordinate) After(other EpisodeCoordinate) bool {
	return c.season > other.season || c.season == other.season && c.episode > other.episode
}

// EpisodePlayback is bounded provider-neutral evidence for one active episode.
type EpisodePlayback struct {
	externalShowID string
	virtualPath    string
	coordinate     EpisodeCoordinate
	viewOffset     time.Duration
	duration       time.Duration
	state          PlaybackState
}

// NewEpisodePlayback validates normalized playback evidence.
func NewEpisodePlayback(
	externalShowID string,
	virtualPath string,
	season int,
	episode int,
	viewOffset time.Duration,
	duration time.Duration,
	state PlaybackState,
) (EpisodePlayback, error) {
	showID, err := validatePlaybackExternalID(externalShowID)
	if err != nil {
		return EpisodePlayback{}, err
	}
	cleanPath, err := validatePlaybackVirtualPath(virtualPath)
	if err != nil {
		return EpisodePlayback{}, err
	}
	coordinate, err := NewEpisodeCoordinate(season, episode)
	if err != nil {
		return EpisodePlayback{}, err
	}
	if duration <= 0 || duration > maximumPlaybackDuration {
		return EpisodePlayback{}, errors.New("playback duration must be positive and no greater than seven days")
	}
	if viewOffset <= 0 || viewOffset > duration {
		return EpisodePlayback{}, errors.New("playback offset must be positive and no greater than duration")
	}
	if state != PlaybackStatePlaying && state != PlaybackStatePaused {
		return EpisodePlayback{}, fmt.Errorf("unsupported playback state: %q", state)
	}
	return EpisodePlayback{
		externalShowID: showID, virtualPath: cleanPath, coordinate: coordinate,
		viewOffset: viewOffset, duration: duration, state: state,
	}, nil
}

func validatePlaybackExternalID(value string) (string, error) {
	clean := strings.TrimSpace(value)
	if clean == "" || clean != value || len(clean) > maximumPlaybackExternalIDBytes {
		return "", errors.New("playback external show ID is invalid")
	}
	for _, character := range clean {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return "", errors.New("playback external show ID is invalid")
		}
	}
	return clean, nil
}

func validatePlaybackVirtualPath(value string) (string, error) {
	if value == "" || len(value) > maximumPlaybackPathBytes || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\\x00") {
		return "", errors.New("playback virtual path is invalid")
	}
	if !strings.HasPrefix(value, "TV Shows/") {
		return "", errors.New("playback virtual path must identify a TV episode")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("playback virtual path is invalid")
		}
	}
	return value, nil
}

// ExternalShowID returns the provider-local show identity.
func (p EpisodePlayback) ExternalShowID() string { return p.externalShowID }

// VirtualPath returns the exact relative BlackPearl media path.
func (p EpisodePlayback) VirtualPath() string { return p.virtualPath }

// Coordinate returns the exact episode coordinate.
func (p EpisodePlayback) Coordinate() EpisodeCoordinate { return p.coordinate }

// ViewOffset returns the observed playback position.
func (p EpisodePlayback) ViewOffset() time.Duration { return p.viewOffset }

// Duration returns the observed episode duration.
func (p EpisodePlayback) Duration() time.Duration { return p.duration }

// State returns the normalized active player state.
func (p EpisodePlayback) State() PlaybackState { return p.state }

// Qualifies reports whether playback crosses both advancement thresholds.
func (p EpisodePlayback) Qualifies(minimumOffset time.Duration, minimumPercent int) bool {
	if minimumOffset <= 0 || minimumPercent < 1 || minimumPercent > 99 {
		return false
	}
	return p.viewOffset >= minimumOffset && p.viewOffset*100 >= p.duration*time.Duration(minimumPercent)
}
