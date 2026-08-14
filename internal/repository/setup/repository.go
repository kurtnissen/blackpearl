// Package setup securely persists browser setup credentials and selection metadata.
package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

const (
	tokenFilename         = "torbox.token"
	configurationFilename = "configuration.json"
	currentFilename       = "current"
	generationsDirectory  = "generations"
	maxTokenBytes         = 4096
	maxConfigurationBytes = 64 * 1024
)

// Repository stores setup state beneath one private directory.
type Repository struct {
	root string
	mu   sync.RWMutex
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
	generations := filepath.Join(root, generationsDirectory)
	if err := os.MkdirAll(generations, 0o700); err != nil {
		return nil, fmt.Errorf("create setup generations directory: %w", err)
	}
	if err := os.Chmod(generations, 0o700); err != nil {
		return nil, fmt.Errorf("protect setup generations directory: %w", err)
	}
	repository := &Repository{root: root}
	current, err := readCurrentGeneration(root)
	if errors.Is(err, os.ErrNotExist) {
		current = ""
	} else if err != nil {
		return nil, errors.New("inspect saved setup generation")
	}
	if err := repository.cleanupInactiveGenerations(current); err != nil {
		return nil, fmt.Errorf("clean inactive setup generations: %w", err)
	}
	return repository, nil
}

// Load reads the saved token and validated non-secret configuration.
func (r *Repository) Load(ctx context.Context) (string, domain.SetupConfiguration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return "", domain.SetupConfiguration{}, fmt.Errorf("load setup: %w", err)
	}
	generation, generationErr := readCurrentGeneration(r.root)
	if errors.Is(generationErr, os.ErrNotExist) {
		return "", domain.SetupConfiguration{}, domain.ErrNotFound
	}
	if generationErr != nil {
		return "", domain.SetupConfiguration{}, errors.New("read saved setup generation")
	}
	generationRoot := filepath.Join(r.root, generationsDirectory, generation)
	token, tokenErr := readBounded(filepath.Join(generationRoot, tokenFilename), maxTokenBytes)
	configurationJSON, configurationErr := readBounded(filepath.Join(generationRoot, configurationFilename), maxConfigurationBytes)
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

// Save atomically commits a pointer to one fully synchronized token and
// configuration generation, so readers can never observe a mixed pair.
func (r *Repository) Save(ctx context.Context, token string, configuration domain.SetupConfiguration) (resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()

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
	generation, err := newGenerationID()
	if err != nil {
		return errors.New("create setup generation identifier")
	}
	generationRoot := filepath.Join(r.root, generationsDirectory, generation)
	if err := os.Mkdir(generationRoot, 0o700); err != nil {
		return errors.New("create setup generation")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := os.RemoveAll(generationRoot); err != nil {
			resultErr = errors.Join(resultErr, errors.New("remove incomplete setup generation"))
		}
		if err := syncDirectory(filepath.Join(r.root, generationsDirectory)); err != nil {
			resultErr = errors.Join(resultErr, errors.New("sync removal of incomplete setup generation"))
		}
	}()
	if err := writeAtomic(filepath.Join(generationRoot, tokenFilename), []byte(token+"\n")); err != nil {
		return errors.New("write setup token")
	}
	if err := writeAtomic(filepath.Join(generationRoot, configurationFilename), content); err != nil {
		return errors.New("write setup configuration")
	}
	if err := syncDirectory(generationRoot); err != nil {
		return errors.New("sync setup generation")
	}
	if err := syncDirectory(filepath.Join(r.root, generationsDirectory)); err != nil {
		return errors.New("sync setup generations directory")
	}
	if err := writeAtomic(filepath.Join(r.root, currentFilename), []byte(generation+"\n")); err != nil {
		return errors.New("commit setup generation")
	}
	committed = true
	if err := syncDirectory(r.root); err != nil {
		return err
	}
	if err := r.cleanupInactiveGenerations(generation); err != nil {
		return errors.Join(domain.ErrCleanupDeferred, fmt.Errorf("clean inactive setup generations: %w", err))
	}
	return nil
}

// Clear removes the committed generation pointer without exposing or deleting
// any partially prepared generation.
func (r *Repository) Clear(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clear setup: %w", err)
	}
	err := os.Remove(filepath.Join(r.root, currentFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("clear saved setup generation")
	}
	if err := syncDirectory(r.root); err != nil {
		return err
	}
	if err := r.cleanupInactiveGenerations(""); err != nil {
		return fmt.Errorf("clean cleared setup generations: %w", err)
	}
	return nil
}

func (r *Repository) cleanupInactiveGenerations(keep string) error {
	directory := filepath.Join(r.root, generationsDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}

func newGenerationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func readCurrentGeneration(root string) (string, error) {
	content, err := readBounded(filepath.Join(root, currentFilename), 128)
	if err != nil {
		return "", err
	}
	generation := strings.TrimSuffix(string(content), "\n")
	decoded, decodeErr := hex.DecodeString(generation)
	if decodeErr != nil || len(decoded) != 16 {
		return "", errors.New("invalid setup generation")
	}
	return generation, nil
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
