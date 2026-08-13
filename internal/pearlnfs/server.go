package pearlnfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

const handleCacheSize = 4096

// Server owns one PearlNFS listener and its serving lifecycle.
type Server struct {
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	serveErr  error
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
	handler := nfshelper.NewCachingHandler(nfshelper.NewNullAuthHandler(filesystem), handleCacheSize)
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
