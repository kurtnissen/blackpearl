// Package setup securely persists browser setup credentials and selection metadata.
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	tokenFilename         = "torbox.token"
	configurationFilename = "configuration.json"
	maxTokenBytes         = 4096
	maxConfigurationBytes = 64 * 1024
)

// Repository stores setup state beneath one private directory.
type Repository struct {
	root string
}

// New creates or repairs the private setup directory.
func New(root string) (*Repository, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("setup directory must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create setup directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect setup directory: %w", err)
	}
	return &Repository{root: root}, nil
}

// Load reads the saved token and validated non-secret configuration.
func (r *Repository) Load(ctx context.Context) (string, domain.SetupConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return "", domain.SetupConfiguration{}, fmt.Errorf("load setup: %w", err)
	}
	token, tokenErr := readBounded(filepath.Join(r.root, tokenFilename), maxTokenBytes)
	configurationJSON, configurationErr := readBounded(filepath.Join(r.root, configurationFilename), maxConfigurationBytes)
	if errors.Is(tokenErr, os.ErrNotExist) && errors.Is(configurationErr, os.ErrNotExist) {
		return "", domain.SetupConfiguration{}, domain.ErrNotFound
	}
	if tokenErr != nil {
		return "", domain.SetupConfiguration{}, errors.New("read saved setup token")
	}
	if configurationErr != nil {
		return "", domain.SetupConfiguration{}, errors.New("read saved setup configuration")
	}
	tokenValue := strings.TrimSuffix(string(token), "\n")
	if err := validateToken(tokenValue); err != nil {
		return "", domain.SetupConfiguration{}, err
	}
	var decoded domain.SetupConfiguration
	if err := json.Unmarshal(configurationJSON, &decoded); err != nil {
		return "", domain.SetupConfiguration{}, errors.New("decode saved setup configuration")
	}
	validated, err := domain.NewSetupConfiguration(decoded.Candidate(), decoded.Title, decoded.Year)
	if err != nil {
		return "", domain.SetupConfiguration{}, fmt.Errorf("validate saved setup configuration: %w", err)
	}
	return tokenValue, validated, nil
}

// Save atomically replaces the private token and non-secret configuration files.
func (r *Repository) Save(ctx context.Context, token string, configuration domain.SetupConfiguration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save setup: %w", err)
	}
	if err := validateToken(token); err != nil {
		return err
	}
	validated, err := domain.NewSetupConfiguration(configuration.Candidate(), configuration.Title, configuration.Year)
	if err != nil {
		return fmt.Errorf("validate setup configuration: %w", err)
	}
	content, err := json.Marshal(validated)
	if err != nil {
		return errors.New("encode setup configuration")
	}
	content = append(content, '\n')
	if err := writeAtomic(filepath.Join(r.root, tokenFilename), []byte(token+"\n")); err != nil {
		return errors.New("write setup token")
	}
	if err := writeAtomic(filepath.Join(r.root, configurationFilename), content); err != nil {
		return errors.New("write setup configuration")
	}
	return syncDirectory(r.root)
}

func validateToken(token string) error {
	if token == "" || len(token) > maxTokenBytes || strings.TrimSpace(token) != token || strings.ContainsRune(token, 0) {
		return errors.New("TorBox token must contain 1 to 4096 bytes without surrounding whitespace")
	}
	return nil
}

func readBounded(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("saved setup file exceeds size limit")
	}
	return content, nil
}

func writeAtomic(filename string, content []byte) (resultErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".blackpearl-setup-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, temporary.Close())
		}
		if resultErr != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open setup directory for sync: %w", err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync setup directory: %w", err)
	}
	return nil
}
