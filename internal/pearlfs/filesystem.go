// Package pearlfs exposes BlackPearl's catalog as a read-only FUSE filesystem.
package pearlfs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Catalog is the business boundary consumed by PearlFS.
type Catalog interface {
	List(ctx context.Context) ([]domain.Media, error)
	Open(ctx context.Context, virtualPath string) (domain.ReadHandle, error)
}

// Root is the read-only PearlFS root node.
type Root struct {
	fs.Inode
	catalog Catalog
	media   []domain.Media
}

// New validates a catalog snapshot before exposing it to the kernel.
func New(ctx context.Context, catalog Catalog) (*Root, error) {
	items, err := catalog.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list catalog for PearlFS: %w", err)
	}
	seen := make(map[string]struct{}, len(items))
	for _, media := range items {
		if err := validateVirtualPath(media.VirtualPath); err != nil {
			return nil, err
		}
		if _, exists := seen[media.VirtualPath]; exists {
			return nil, fmt.Errorf("duplicate virtual path: %q", media.VirtualPath)
		}
		seen[media.VirtualPath] = struct{}{}
	}
	sort.Slice(items, func(left int, right int) bool {
		return items[left].VirtualPath < items[right].VirtualPath
	})
	return &Root{catalog: catalog, media: items}, nil
}

func validateVirtualPath(virtualPath string) error {
	parts := strings.Split(virtualPath, "/")
	if len(parts) != 3 || parts[0] != "Movies" || path.Clean(virtualPath) != virtualPath {
		return fmt.Errorf("invalid virtual path: %q", virtualPath)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid virtual path: %q", virtualPath)
		}
	}
	return nil
}

func (r *Root) virtualPaths() []string {
	result := make([]string, 0, len(r.media))
	for _, media := range r.media {
		result = append(result, media.VirtualPath)
	}
	return result
}

// OnAdd materializes the validated catalog snapshot as persistent FUSE inodes.
func (r *Root) OnAdd(ctx context.Context) {
	movies := r.NewPersistentInode(
		ctx,
		&directoryNode{},
		fs.StableAttr{Mode: syscall.S_IFDIR, Ino: inodeFor("Movies")},
	)
	r.AddChild("Movies", movies, false)
	directories := make(map[string]*fs.Inode)
	for _, media := range r.media {
		parts := strings.Split(media.VirtualPath, "/")
		directoryPath := strings.Join(parts[:2], "/")
		parent, exists := directories[directoryPath]
		if !exists {
			parent = movies.NewPersistentInode(
				ctx,
				&directoryNode{},
				fs.StableAttr{Mode: syscall.S_IFDIR, Ino: inodeFor(directoryPath)},
			)
			movies.AddChild(parts[1], parent, false)
			directories[directoryPath] = parent
		}
		file := parent.NewPersistentInode(
			ctx,
			&fileNode{media: media, catalog: r.catalog},
			fs.StableAttr{Mode: syscall.S_IFREG, Ino: inodeFor(media.VirtualPath)},
		)
		parent.AddChild(parts[2], file, false)
	}
}

// Getattr reports a traversable read-only root directory.
func (r *Root) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	return 0
}

type directoryNode struct {
	fs.Inode
}

func (n *directoryNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	return 0
}

type fileNode struct {
	fs.Inode
	media   domain.Media
	catalog Catalog
}

func (n *fileNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFREG | 0o444
	out.Size = uint64(n.media.Size)
	out.Blksize = 4096
	out.Blocks = uint64((n.media.Size + 511) / 512)
	return 0
}

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		return nil, 0, syscall.EROFS
	}
	reader, err := n.catalog.Open(ctx, n.media.VirtualPath)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, 0, syscall.ENOENT
	}
	if err != nil {
		return nil, 0, syscall.EIO
	}
	return &fileHandle{reader: reader}, fuse.FOPEN_KEEP_CACHE, 0
}

type fileHandle struct {
	reader domain.ReadHandle
}

func (h *fileHandle) Read(ctx context.Context, destination []byte, offset int64) (fuse.ReadResult, syscall.Errno) {
	if offset < 0 {
		return nil, syscall.EINVAL
	}
	count, err := h.reader.ReadAt(ctx, destination, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fs.ToErrno(err)
	}
	return fuse.ReadResultData(destination[:count]), 0
}

func (h *fileHandle) Release(_ context.Context) syscall.Errno {
	return fs.ToErrno(h.reader.Close())
}

func inodeFor(virtualPath string) uint64 {
	hasher := fnv.New64a()
	if _, err := hasher.Write([]byte(virtualPath)); err != nil {
		return 2
	}
	value := hasher.Sum64()
	if value < 2 {
		return value + 2
	}
	return value
}

// Mount mounts PearlFS and starts its request server.
func Mount(ctx context.Context, mountPath string, root *Root) (*fuse.Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mount PearlFS: %w", err)
	}
	if !filepath.IsAbs(mountPath) {
		return nil, fmt.Errorf("PearlFS mount path must be absolute: %q", mountPath)
	}
	if root == nil {
		return nil, errors.New("PearlFS root is required")
	}
	server, err := fs.Mount(mountPath, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther:    true,
			Options:       []string{"ro", "default_permissions"},
			FsName:        "blackpearl",
			Name:          "blackpearl",
			DisableXAttrs: true,
		},
		NullPermissions: true,
	})
	if err != nil {
		return nil, fmt.Errorf("mount PearlFS at %s: %w", mountPath, err)
	}
	return server, nil
}

var (
	_ fs.NodeOnAdder   = (*Root)(nil)
	_ fs.NodeGetattrer = (*Root)(nil)
	_ fs.NodeGetattrer = (*directoryNode)(nil)
	_ fs.NodeGetattrer = (*fileNode)(nil)
	_ fs.NodeOpener    = (*fileNode)(nil)
	_ fs.FileReader    = (*fileHandle)(nil)
	_ fs.FileReleaser  = (*fileHandle)(nil)
)
