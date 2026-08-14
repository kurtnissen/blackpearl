// Package pearlnfs exposes BlackPearl's catalog as a read-only NFS filesystem.
package pearlnfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	"github.com/go-git/go-billy/v5"
)

// Catalog is the business boundary consumed by PearlNFS.
type Catalog interface {
	List(ctx context.Context) ([]domain.Media, error)
	Open(ctx context.Context, virtualPath string) (domain.ReadHandle, error)
}

type filesystem struct {
	ctx        context.Context
	entriesMu  sync.RWMutex
	catalog    Catalog
	entries    map[string]entry
	generation uint64
	modified   time.Time
}

// Reloadable is a read-only filesystem whose namespace can be atomically refreshed.
type Reloadable interface {
	billy.Filesystem
	Reload(ctx context.Context) error
	Replace(ctx context.Context, catalog Catalog) (Catalog, error)
}

type entry struct {
	name     string
	media    *domain.Media
	children []string
	modified time.Time
}

// New validates a catalog snapshot and adapts it to a read-only Billy filesystem.
func New(ctx context.Context, catalog Catalog) (billy.Filesystem, error) {
	return NewReloadable(ctx, catalog)
}

// NewReloadable validates a catalog snapshot and returns a reloadable filesystem.
func NewReloadable(ctx context.Context, catalog Catalog) (Reloadable, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create PearlNFS filesystem: %w", err)
	}
	if catalog == nil {
		return nil, errors.New("PearlNFS catalog is required")
	}
	result := &filesystem{
		ctx:     ctx,
		catalog: catalog,
	}
	if err := result.Reload(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// Reload builds a complete namespace off-lock and publishes it atomically.
func (f *filesystem) Reload(ctx context.Context) error {
	f.entriesMu.RLock()
	catalog := f.catalog
	f.entriesMu.RUnlock()
	_, err := f.Replace(ctx, catalog)
	return err
}

// Replace publishes one immutable namespace-and-catalog snapshot and returns
// the catalog previously used for file opens.
func (f *filesystem) Replace(ctx context.Context, catalog Catalog) (Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("replace PearlNFS filesystem: %w", err)
	}
	if catalog == nil {
		return nil, errors.New("replacement PearlNFS catalog is required")
	}
	items, err := catalog.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list catalog for PearlNFS: %w", err)
	}
	entries, err := buildEntries(items)
	if err != nil {
		return nil, err
	}
	f.entriesMu.Lock()
	modified := time.Now().UTC().Truncate(time.Second)
	if !modified.After(f.modified) {
		modified = f.modified.Add(time.Second)
	}
	for key, value := range entries {
		value.modified = modified
		entries[key] = value
	}
	previous := f.catalog
	f.catalog = catalog
	f.entries = entries
	f.generation++
	f.modified = modified
	f.entriesMu.Unlock()
	return previous, nil
}

func buildEntries(items []domain.Media) (map[string]entry, error) {
	builder := &filesystem{entries: map[string]entry{"": {name: "/"}}}
	for index := range items {
		media := items[index]
		if err := builder.addMedia(media); err != nil {
			return nil, err
		}
	}
	for key, value := range builder.entries {
		sort.Strings(value.children)
		builder.entries[key] = value
	}
	return builder.entries, nil
}

func (f *filesystem) addMedia(media domain.Media) error {
	parts := strings.Split(media.VirtualPath, "/")
	moviePath := len(parts) == 3 && parts[0] == "Movies"
	episodePath := len(parts) == 4 && parts[0] == "TV Shows"
	if (!moviePath && !episodePath) || path.Clean(media.VirtualPath) != media.VirtualPath {
		return fmt.Errorf("invalid virtual path: %q", media.VirtualPath)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid virtual path: %q", media.VirtualPath)
		}
	}
	if _, exists := f.entries[media.VirtualPath]; exists {
		return fmt.Errorf("duplicate virtual path: %q", media.VirtualPath)
	}
	for index := 0; index < len(parts)-1; index++ {
		parent := strings.Join(parts[:index], "/")
		f.addDirectory(parent, parts[index])
	}
	mediaCopy := media
	filename := parts[len(parts)-1]
	f.entries[media.VirtualPath] = entry{name: filename, media: &mediaCopy}
	parent := strings.Join(parts[:len(parts)-1], "/")
	f.addChild(parent, filename)
	return nil
}

func (f *filesystem) addDirectory(parent string, name string) {
	virtualPath := cleanPath(path.Join(parent, name))
	if _, exists := f.entries[virtualPath]; !exists {
		f.entries[virtualPath] = entry{name: name}
	}
	f.addChild(parent, name)
}

func (f *filesystem) addChild(parent string, child string) {
	parent = cleanPath(parent)
	value := f.entries[parent]
	for _, existing := range value.children {
		if existing == child {
			return
		}
	}
	value.children = append(value.children, child)
	f.entries[parent] = value
}

func (f *filesystem) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.SeekCapability
}

func (f *filesystem) Create(string) (billy.File, error) {
	return nil, billy.ErrReadOnly
}

func (f *filesystem) Open(filename string) (billy.File, error) {
	virtualPath := cleanPath(filename)
	f.entriesMu.RLock()
	value, exists := f.entries[virtualPath]
	catalog := f.catalog
	f.entriesMu.RUnlock()
	if !exists {
		return nil, os.ErrNotExist
	}
	if value.media == nil {
		return nil, &os.PathError{Op: "open", Path: filename, Err: errors.New("is a directory")}
	}
	handle, err := catalog.Open(f.ctx, virtualPath)
	if err != nil {
		return nil, fmt.Errorf("open PearlNFS media %q: %w", virtualPath, err)
	}
	return &mediaFile{ctx: f.ctx, name: virtualPath, handle: handle}, nil
}

func (f *filesystem) handleSnapshot(filename string) (billy.Filesystem, string) {
	virtualPath := cleanPath(filename)
	f.entriesMu.RLock()
	value, exists := f.entries[virtualPath]
	if !exists || value.media == nil {
		f.entriesMu.RUnlock()
		return f, "directory\x00" + virtualPath
	}
	frozen := &filesystem{ctx: f.ctx, catalog: f.catalog, entries: f.entries, generation: f.generation}
	media := *value.media
	f.entriesMu.RUnlock()
	identity := fmt.Sprintf("file\x00%s\x00%s\x00%d\x00%s\x00%s", media.ID, media.VirtualPath, media.Size, media.Backing.Provider, media.Backing.ObjectID)
	return frozen, identity
}

func (f *filesystem) handlePaths() []string {
	f.entriesMu.RLock()
	paths := make([]string, 0, len(f.entries))
	for virtualPath := range f.entries {
		paths = append(paths, virtualPath)
	}
	f.entriesMu.RUnlock()
	sort.Strings(paths)
	return paths
}

func (f *filesystem) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_CREATE != 0 || flag&os.O_TRUNC != 0 {
		return nil, billy.ErrReadOnly
	}
	return f.Open(filename)
}

func (f *filesystem) Stat(filename string) (os.FileInfo, error) {
	f.entriesMu.RLock()
	value, exists := f.entries[cleanPath(filename)]
	f.entriesMu.RUnlock()
	if !exists {
		return nil, os.ErrNotExist
	}
	return infoFor(value), nil
}

func (f *filesystem) Rename(string, string) error {
	return billy.ErrReadOnly
}

func (f *filesystem) Remove(string) error {
	return billy.ErrReadOnly
}

func (f *filesystem) Join(elements ...string) string {
	return path.Join(elements...)
}

func (f *filesystem) TempFile(string, string) (billy.File, error) {
	return nil, billy.ErrReadOnly
}

func (f *filesystem) ReadDir(dirname string) ([]os.FileInfo, error) {
	virtualPath := cleanPath(dirname)
	f.entriesMu.RLock()
	defer f.entriesMu.RUnlock()
	directory, exists := f.entries[virtualPath]
	if !exists {
		return nil, os.ErrNotExist
	}
	if directory.media != nil {
		return nil, &os.PathError{Op: "readdir", Path: dirname, Err: errors.New("not a directory")}
	}
	children := make([]os.FileInfo, 0, len(directory.children))
	for _, child := range directory.children {
		children = append(children, infoFor(f.entries[cleanPath(path.Join(virtualPath, child))]))
	}
	return children, nil
}

func (f *filesystem) MkdirAll(string, os.FileMode) error {
	return billy.ErrReadOnly
}

func (f *filesystem) Lstat(filename string) (os.FileInfo, error) {
	return f.Stat(filename)
}

func (f *filesystem) Symlink(string, string) error {
	return billy.ErrReadOnly
}

func (f *filesystem) Readlink(string) (string, error) {
	return "", billy.ErrNotSupported
}

func (f *filesystem) Chroot(dirname string) (billy.Filesystem, error) {
	if cleanPath(dirname) != "" {
		return nil, os.ErrNotExist
	}
	return f, nil
}

func (f *filesystem) Root() string {
	return "/"
}

func cleanPath(filename string) string {
	cleaned := path.Clean("/" + filename)
	if cleaned == "/" {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func infoFor(value entry) os.FileInfo {
	if value.media == nil {
		return fileInfo{name: value.name, directory: true, modified: value.modified}
	}
	return fileInfo{name: value.name, size: value.media.Size, modified: value.modified}
}

type mediaFile struct {
	ctx       context.Context
	name      string
	handle    domain.ReadHandle
	offset    int64
	offsetMu  sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (f *mediaFile) Name() string {
	return f.name
}

func (f *mediaFile) Write([]byte) (int, error) {
	return 0, billy.ErrReadOnly
}

func (f *mediaFile) Read(destination []byte) (int, error) {
	f.offsetMu.Lock()
	defer f.offsetMu.Unlock()
	count, err := f.handle.ReadAt(f.ctx, destination, f.offset)
	f.offset += int64(count)
	return count, err
}

func (f *mediaFile) ReadAt(destination []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	return f.handle.ReadAt(f.ctx, destination, offset)
}

func (f *mediaFile) Seek(offset int64, whence int) (int64, error) {
	f.offsetMu.Lock()
	defer f.offsetMu.Unlock()
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.handle.Size() + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if next < 0 {
		return 0, errors.New("negative position")
	}
	f.offset = next
	return next, nil
}

func (f *mediaFile) Close() error {
	f.closeOnce.Do(func() {
		f.closeErr = f.handle.Close()
	})
	return f.closeErr
}

func (f *mediaFile) Lock() error {
	return nil
}

func (f *mediaFile) Unlock() error {
	return nil
}

func (f *mediaFile) Truncate(int64) error {
	return billy.ErrReadOnly
}

type fileInfo struct {
	name      string
	size      int64
	directory bool
	modified  time.Time
}

func (i fileInfo) Name() string {
	return i.name
}

func (i fileInfo) Size() int64 {
	return i.size
}

func (i fileInfo) Mode() os.FileMode {
	if i.directory {
		return os.ModeDir | 0o555
	}
	return 0o444
}

func (i fileInfo) ModTime() time.Time {
	return i.modified
}

func (i fileInfo) IsDir() bool {
	return i.directory
}

func (i fileInfo) Sys() any {
	return nil
}

var _ billy.Filesystem = (*filesystem)(nil)
var _ Reloadable = (*filesystem)(nil)
var _ billy.Chroot = (*filesystem)(nil)
var _ billy.Basic = (*filesystem)(nil)
var _ billy.Dir = (*filesystem)(nil)
var _ billy.Symlink = (*filesystem)(nil)
var _ billy.File = (*mediaFile)(nil)
