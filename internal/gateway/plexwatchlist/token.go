// Package plexwatchlist reads Plex watchlist metadata through a narrow,
// credential-safe gateway.
package plexwatchlist

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maximumPreferencesBytes = 1 << 20
	maximumTokenBytes       = 4 << 10
)

// TokenSource supplies a current Plex account token for one gateway request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type tokenSourceFormat uint8

const (
	preferencesFormat tokenSourceFormat = iota + 1
	tokenFileFormat
)

type fileTokenSource struct {
	path   string
	format tokenSourceFormat
}

// NewPreferencesTokenSource reads PlexOnlineToken from a bounded Plex
// Preferences.xml file on every request.
func NewPreferencesTokenSource(path string) (TokenSource, error) {
	return newFileTokenSource(path, preferencesFormat)
}

// NewTokenFileSource reads a dedicated bounded Plex token file on every
// request.
func NewTokenFileSource(path string) (TokenSource, error) {
	return newFileTokenSource(path, tokenFileFormat)
}

func newFileTokenSource(path string, format tokenSourceFormat) (TokenSource, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Plex token source path must be absolute")
	}
	return &fileTokenSource{path: path, format: format}, nil
}

func (s *fileTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("read Plex watchlist credential: %w", err)
	}
	maximum := int64(maximumPreferencesBytes)
	if s.format == tokenFileFormat {
		maximum = maximumTokenBytes
	}
	content, err := readBoundedFile(s.path, maximum)
	if err != nil {
		return "", errors.New("read Plex watchlist credential")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("read Plex watchlist credential: %w", err)
	}
	token := ""
	switch s.format {
	case preferencesFormat:
		var preferences struct {
			Token string `xml:"PlexOnlineToken,attr"`
		}
		if err := xml.Unmarshal(content, &preferences); err != nil {
			return "", errors.New("decode Plex watchlist credential")
		}
		token = preferences.Token
	case tokenFileFormat:
		token = string(content)
	default:
		return "", errors.New("unsupported Plex watchlist credential source")
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maximumTokenBytes {
		return "", errors.New("validate Plex watchlist credential")
	}
	for _, character := range token {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return "", errors.New("validate Plex watchlist credential")
		}
	}
	return token, nil
}

func readBoundedFile(path string, maximum int64) (result []byte, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("credential source exceeds limit")
	}
	return content, nil
}
