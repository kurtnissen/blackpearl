package pearlnfs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

const handleCacheSize = 4096

// Server owns one PearlNFS listener and its serving lifecycle.
type Server struct {
	listener   net.Listener
	reloadable Reloadable
	handles    *stableHandleHandler
	done       chan struct{}
	closeOnce  sync.Once
	closeErr   error
	serveErr   error
}

type handleSnapshotter interface {
	handleSnapshot(filename string) (billy.Filesystem, string)
	handlePaths() []string
}

type stableHandleHandler struct {
	nfs.Handler
	live        billy.Filesystem
	snapshotter handleSnapshotter
	mu          sync.RWMutex
	handles     map[[sha256.Size]byte]stableHandleEntry
}

type stableHandleEntry struct {
	filesystem billy.Filesystem
	path       []string
}

func (h *stableHandleHandler) ToHandle(filesystem billy.Filesystem, path []string) []byte {
	identity := "path\x00" + filesystem.Join(path...)
	if filesystem == h.live {
		filesystem, identity = h.snapshotter.handleSnapshot(filesystem.Join(path...))
	}
	handle := sha256.Sum256([]byte(identity))
	pathCopy := append([]string(nil), path...)
	h.mu.Lock()
	h.handles[handle] = stableHandleEntry{filesystem: filesystem, path: pathCopy}
	h.mu.Unlock()
	return append([]byte(nil), handle[:]...)
}

func (h *stableHandleHandler) FromHandle(value []byte) (billy.Filesystem, []string, error) {
	if len(value) != sha256.Size {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	var handle [sha256.Size]byte
	copy(handle[:], value)
	h.mu.RLock()
	entry, ok := h.handles[handle]
	h.mu.RUnlock()
	if !ok {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	return entry.filesystem, append([]string(nil), entry.path...), nil
}

func (*stableHandleHandler) InvalidateHandle(billy.Filesystem, []byte) error { return nil }
func (*stableHandleHandler) HandleLimit() int                                { return handleCacheSize }

func (h *stableHandleHandler) registerCurrent() {
	for _, virtualPath := range h.snapshotter.handlePaths() {
		path := []string{virtualPath}
		if virtualPath != "" {
			path = strings.Split(virtualPath, "/")
		}
		h.ToHandle(h.live, path)
	}
}

// Start binds a read-only NFSv3 server for the supplied filesystem.
func Start(ctx context.Context, address string, filesystem billy.Filesystem) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start PearlNFS: %w", err)
	}
	if filesystem == nil {
		return nil, errors.New("PearlNFS filesystem is required")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for PearlNFS on %s: %w", address, err)
	}
	server := &Server{listener: listener, done: make(chan struct{})}
	if reloadable, ok := filesystem.(Reloadable); ok {
		server.reloadable = reloadable
	}
	baseHandler := nfshelper.NewNullAuthHandler(filesystem)
	handler := nfs.Handler(nfshelper.NewCachingHandler(baseHandler, handleCacheSize))
	if snapshotter, ok := filesystem.(handleSnapshotter); ok {
		server.handles = &stableHandleHandler{
			Handler: baseHandler, live: filesystem, snapshotter: snapshotter,
			handles: make(map[[sha256.Size]byte]stableHandleEntry),
		}
		server.handles.registerCurrent()
		handler = server.handles
	}
	protocolServer := &nfs.Server{Handler: handler, Context: ctx}
	go func() {
		server.serveErr = protocolServer.Serve(listener)
		close(server.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-server.done:
		}
	}()
	return server, nil
}

// Replace atomically publishes one namespace-and-catalog snapshot.
func (s *Server) Replace(ctx context.Context, catalog Catalog) (Catalog, error) {
	if s.reloadable == nil {
		return nil, errors.New("PearlNFS filesystem does not support replacement")
	}
	previous, err := s.reloadable.Replace(ctx, catalog)
	if err != nil {
		return nil, fmt.Errorf("replace PearlNFS: %w", err)
	}
	if s.handles != nil {
		s.handles.registerCurrent()
	}
	return previous, nil
}

// Reload atomically refreshes the exported namespace from its catalog.
func (s *Server) Reload(ctx context.Context) error {
	if s.reloadable == nil {
		return errors.New("PearlNFS filesystem does not support reload")
	}
	if err := s.reloadable.Reload(ctx); err != nil {
		return fmt.Errorf("reload PearlNFS: %w", err)
	}
	if s.handles != nil {
		s.handles.registerCurrent()
	}
	return nil
}

// Addr reports the bound NFS listener address.
func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

// Close stops accepting new NFS connections.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.listener.Close()
	})
	if errors.Is(s.closeErr, net.ErrClosed) {
		return nil
	}
	return s.closeErr
}

// Wait blocks until the NFS listener exits.
func (s *Server) Wait() error {
	<-s.done
	if errors.Is(s.serveErr, net.ErrClosed) {
		return nil
	}
	return s.serveErr
}
