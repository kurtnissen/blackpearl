package main

import (
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

const logicalFileName = "logical-1TiB.bin"

func main() {
	listener, err := net.Listen("tcp", ":20492")
	if err != nil {
		log.Fatal(err)
	}
	filesystem := newGeneratedFilesystem(1 << 40)
	handler := nfshelper.NewCachingHandler(nfshelper.NewNullAuthHandler(filesystem), 32)
	if err := nfs.Serve(listener, handler); err != nil {
		log.Fatal(err)
	}
}

type generatedFilesystem struct {
	logicalSize int64
}

func newGeneratedFilesystem(logicalSize int64) billy.Filesystem {
	return &generatedFilesystem{logicalSize: logicalSize}
}

func (f *generatedFilesystem) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.SeekCapability
}

func (f *generatedFilesystem) Create(string) (billy.File, error) {
	return nil, billy.ErrReadOnly
}

func (f *generatedFilesystem) Open(filename string) (billy.File, error) {
	if clean(filename) != logicalFileName {
		return nil, os.ErrNotExist
	}
	return &generatedFile{name: logicalFileName, size: f.logicalSize}, nil
}

func (f *generatedFilesystem) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 {
		return nil, billy.ErrReadOnly
	}
	return f.Open(filename)
}

func (f *generatedFilesystem) Stat(filename string) (os.FileInfo, error) {
	switch clean(filename) {
	case "":
		return generatedInfo{name: "/", directory: true}, nil
	case logicalFileName:
		return generatedInfo{name: logicalFileName, size: f.logicalSize}, nil
	default:
		return nil, os.ErrNotExist
	}
}

func (f *generatedFilesystem) Rename(string, string) error {
	return billy.ErrReadOnly
}

func (f *generatedFilesystem) Remove(string) error {
	return billy.ErrReadOnly
}

func (f *generatedFilesystem) Join(elements ...string) string {
	return path.Join(elements...)
}

func (f *generatedFilesystem) TempFile(string, string) (billy.File, error) {
	return nil, billy.ErrReadOnly
}

func (f *generatedFilesystem) ReadDir(dirname string) ([]os.FileInfo, error) {
	if clean(dirname) != "" {
		return nil, os.ErrNotExist
	}
	return []os.FileInfo{generatedInfo{name: logicalFileName, size: f.logicalSize}}, nil
}

func (f *generatedFilesystem) MkdirAll(string, os.FileMode) error {
	return billy.ErrReadOnly
}

func (f *generatedFilesystem) Lstat(filename string) (os.FileInfo, error) {
	return f.Stat(filename)
}

func (f *generatedFilesystem) Symlink(string, string) error {
	return billy.ErrReadOnly
}

func (f *generatedFilesystem) Readlink(string) (string, error) {
	return "", billy.ErrNotSupported
}

func (f *generatedFilesystem) Chroot(dirname string) (billy.Filesystem, error) {
	if clean(dirname) != "" {
		return nil, os.ErrNotExist
	}
	return f, nil
}

func (f *generatedFilesystem) Root() string {
	return "/"
}

func clean(filename string) string {
	cleaned := path.Clean("/" + filename)
	if cleaned == "/" {
		return ""
	}
	return cleaned[1:]
}

type generatedFile struct {
	name   string
	size   int64
	offset int64
	mu     sync.Mutex
}

func (f *generatedFile) Name() string {
	return f.name
}

func (f *generatedFile) Write([]byte) (int, error) {
	return 0, billy.ErrReadOnly
}

func (f *generatedFile) Read(destination []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count, err := f.ReadAt(destination, f.offset)
	f.offset += int64(count)
	return count, err
}

func (f *generatedFile) ReadAt(destination []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	if offset >= f.size {
		return 0, io.EOF
	}
	count := min(int64(len(destination)), f.size-offset)
	for index := int64(0); index < count; index++ {
		destination[index] = byte(offset + index)
	}
	if count < int64(len(destination)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func (f *generatedFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if next < 0 {
		return 0, errors.New("negative position")
	}
	f.offset = next
	return next, nil
}

func (f *generatedFile) Close() error {
	return nil
}

func (f *generatedFile) Lock() error {
	return nil
}

func (f *generatedFile) Unlock() error {
	return nil
}

func (f *generatedFile) Truncate(int64) error {
	return billy.ErrReadOnly
}

type generatedInfo struct {
	name      string
	size      int64
	directory bool
}

func (i generatedInfo) Name() string {
	return i.name
}

func (i generatedInfo) Size() int64 {
	return i.size
}

func (i generatedInfo) Mode() os.FileMode {
	if i.directory {
		return os.ModeDir | 0o555
	}
	return 0o444
}

func (i generatedInfo) ModTime() time.Time {
	return time.Unix(0, 0)
}

func (i generatedInfo) IsDir() bool {
	return i.directory
}

func (i generatedInfo) Sys() any {
	return nil
}
