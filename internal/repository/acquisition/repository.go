// Package acquisition persists private acquisition-provider settings.
package acquisition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	acquisitiondomain "github.com/blackpearl-media/blackpearl/internal/acquisition"
	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	settingsFilename     = "search-provider.json"
	maximumSettingsBytes = 16 * 1024
)

// Repository stores one private search-provider connection.
type Repository struct {
	root string
	mu   sync.RWMutex
}

type persistedSettings struct {
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Credential string `json:"credential"`
}

// New creates or repairs the private acquisition settings directory.
func New(root string) (*Repository, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("acquisition settings directory must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("create acquisition settings directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, errors.New("protect acquisition settings directory")
	}
	settingsPath := filepath.Join(root, settingsFilename)
	if _, err := os.Stat(settingsPath); err == nil {
		if err := os.Chmod(settingsPath, 0o600); err != nil {
			return nil, errors.New("protect acquisition settings file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("inspect acquisition settings file")
	}
	return &Repository{root: root}, nil
}

// Load reads and validates the saved search-provider connection.
func (r *Repository) Load(ctx context.Context) (acquisitiondomain.SearchProviderSettings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return acquisitiondomain.SearchProviderSettings{}, fmt.Errorf("load acquisition settings: %w", err)
	}
	content, err := readBounded(filepath.Join(r.root, settingsFilename), maximumSettingsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return acquisitiondomain.SearchProviderSettings{}, domain.ErrNotFound
	}
	if err != nil {
		return acquisitiondomain.SearchProviderSettings{}, errors.New("read acquisition settings")
	}
	var decoded persistedSettings
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return acquisitiondomain.SearchProviderSettings{}, errors.New("decode acquisition settings")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return acquisitiondomain.SearchProviderSettings{}, errors.New("decode acquisition settings")
	}
	settings, err := acquisitiondomain.NewSearchProviderSettings(decoded.Provider, decoded.Endpoint, decoded.Credential)
	if err != nil {
		return acquisitiondomain.SearchProviderSettings{}, errors.New("validate acquisition settings")
	}
	return settings, nil
}

// Save atomically replaces the private search-provider connection.
func (r *Repository) Save(ctx context.Context, settings acquisitiondomain.SearchProviderSettings) error {
	validated, err := acquisitiondomain.NewSearchProviderSettings(settings.Provider(), settings.Endpoint(), settings.Credential())
	if err != nil {
		return errors.New("validate acquisition settings")
	}
	content, err := json.Marshal(persistedSettings{
		Provider: validated.Provider(), Endpoint: validated.Endpoint(), Credential: validated.Credential(),
	})
	if err != nil {
		return errors.New("encode acquisition settings")
	}
	content = append(content, '\n')
	if len(content) > maximumSettingsBytes {
		return errors.New("acquisition settings exceed storage limit")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save acquisition settings: %w", err)
	}
	return r.writeAtomic(ctx, content)
}

func (r *Repository) writeAtomic(ctx context.Context, content []byte) (resultErr error) {
	temporary, err := os.CreateTemp(r.root, ".search-provider-*")
	if err != nil {
		return errors.New("create temporary acquisition settings")
	}
	temporaryPath := temporary.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, temporary.Close())
		}
		if !committed {
			if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, errors.New("remove temporary acquisition settings"))
			}
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("protect temporary acquisition settings")
	}
	if _, err := temporary.Write(content); err != nil {
		return errors.New("write temporary acquisition settings")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync temporary acquisition settings")
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return errors.New("close temporary acquisition settings")
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save acquisition settings: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(r.root, settingsFilename)); err != nil {
		return errors.New("commit acquisition settings")
	}
	committed = true
	if err := syncDirectory(r.root); err != nil {
		return errors.New("sync acquisition settings directory")
	}
	return nil
}

func readBounded(path string, maximum int64) (result []byte, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, errors.New("read bounded acquisition settings")
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("acquisition settings exceed storage limit")
	}
	return content, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		closeErr := directory.Close()
		return errors.Join(err, closeErr)
	}
	return directory.Close()
}
