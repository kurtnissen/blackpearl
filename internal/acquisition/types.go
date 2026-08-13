// Package acquisition defines provider-neutral retrieval contracts.
package acquisition

import (
	"errors"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// ByteRange is a requested contiguous object range.
type ByteRange struct {
	Offset int64
	Length int64
}

// NewByteRange validates a requested byte range.
func NewByteRange(offset int64, length int64) (ByteRange, error) {
	if offset < 0 {
		return ByteRange{}, errors.New("byte range offset must not be negative")
	}
	if length <= 0 {
		return ByteRange{}, errors.New("byte range length must be positive")
	}
	return ByteRange{Offset: offset, Length: length}, nil
}

// Request describes media sought from any authorized acquisition provider.
type Request struct {
	MediaID     domain.MediaID
	VirtualPath string
	Ranges      []ByteRange
}

// Candidate is a normalized provider result.
type Candidate struct {
	Provider string
	ObjectID string
	Size     int64
	Cached   bool
}
