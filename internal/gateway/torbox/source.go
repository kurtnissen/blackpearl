package torbox

import (
	"context"
	"errors"
	"io"
	"net/url"
	"sync/atomic"
)

type source struct {
	gateway    *Gateway
	identifier objectID
	metadata   fileMetadata
	download   atomic.Pointer[url.URL]
	closed     atomic.Bool
}

func newSource(gateway *Gateway, identifier objectID, metadata fileMetadata, downloadURL *url.URL) *source {
	result := &source{gateway: gateway, identifier: identifier, metadata: metadata}
	result.download.Store(downloadURL)
	return result
}

func (s *source) ReadAt(context.Context, []byte, int64) (int, error) {
	return 0, errors.New("TorBox range reads are not implemented")
}

func (s *source) Size() int64 { return s.metadata.size }

func (s *source) Validator() string { return s.metadata.validator }

func (s *source) Close() error {
	s.closed.Store(true)
	return nil
}

var _ interface {
	ReadAt(context.Context, []byte, int64) (int, error)
	Size() int64
	Validator() string
	Close() error
} = (*source)(nil)

var _ = io.EOF
