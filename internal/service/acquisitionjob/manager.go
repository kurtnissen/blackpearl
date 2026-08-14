// Package acquisitionjob orchestrates durable background media acquisition.
package acquisitionjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/acquisition"
)

// ManagerRepository is the persistence boundary consumed by the paired API.
type ManagerRepository interface {
	Submit(ctx context.Context, id string, request acquisition.SearchRequest, now time.Time) (acquisition.AcquisitionJob, bool, error)
	Get(ctx context.Context, id string) (acquisition.AcquisitionJob, error)
	List(ctx context.Context, limit int) ([]acquisition.AcquisitionJob, error)
}

// ManagerOptions provides testable time and random-ID sources.
type ManagerOptions struct {
	Now   func() time.Time
	NewID func() (string, error)
}

// Manager exposes privacy-safe job submission and reads.
type Manager struct {
	repository ManagerRepository
	now        func() time.Time
	newID      func() (string, error)
}

// NewManager constructs the durable job API service.
func NewManager(repository ManagerRepository, options ManagerOptions) (*Manager, error) {
	if repository == nil {
		return nil, errors.New("acquisition job manager repository is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomJobID
	}
	return &Manager{repository: repository, now: now, newID: newID}, nil
}

// Submit creates or deduplicates one active explicit preparation request.
func (m *Manager) Submit(ctx context.Context, request acquisition.SearchRequest) (acquisition.AcquisitionJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("submit background acquisition: %w", err)
	}
	id, err := m.newID()
	if err != nil {
		return acquisition.AcquisitionJob{}, false, errors.New("generate background acquisition ID")
	}
	job, created, err := m.repository.Submit(ctx, id, request, m.now().UTC())
	if err != nil {
		return acquisition.AcquisitionJob{}, false, fmt.Errorf("persist background acquisition: %w", err)
	}
	return job, created, nil
}

// Get returns one privacy-safe durable job.
func (m *Manager) Get(ctx context.Context, id string) (acquisition.AcquisitionJob, error) {
	job, err := m.repository.Get(ctx, id)
	if err != nil {
		return acquisition.AcquisitionJob{}, fmt.Errorf("get background acquisition: %w", err)
	}
	return job, nil
}

// List returns recent jobs for the already paired local browser.
func (m *Manager) List(ctx context.Context, limit int) ([]acquisition.AcquisitionJob, error) {
	jobs, err := m.repository.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list background acquisitions: %w", err)
	}
	return jobs, nil
}

func randomJobID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read secure randomness: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
