package ftpserver

import (
	"io"
	"io/fs"
	"os"
	"time"

	"goftp.io/server/v2"
)

func (perm *singlePerm) GetOwner(s string) (string, error)        { return "", nil }
func (perm *singlePerm) GetGroup(s string) (string, error)        { return "", nil }
func (perm *singlePerm) GetMode(s string) (os.FileMode, error)    { return 0644, nil }
func (perm *singlePerm) ChOwner(s, owner string) error            { return nil }
func (perm *singlePerm) ChGroup(s, group string) error            { return nil }
func (perm *singlePerm) ChMode(s string, mode os.FileMode) error  { return nil }

type dummyFileInfo struct{}

func (d *dummyFileInfo) Name() string       { return "dummy" }
func (d *dummyFileInfo) Size() int64        { return 0 }
func (d *dummyFileInfo) Mode() fs.FileMode  { return 0 }
func (d *dummyFileInfo) ModTime() time.Time { return time.Time{} }
func (d *dummyFileInfo) IsDir() bool        { return false }
func (d *dummyFileInfo) Sys() interface{}   { return nil }

type dummyDirInfo struct{}

func (d *dummyDirInfo) Name() string       { return "dummy" }
func (d *dummyDirInfo) Size() int64        { return 0 }
func (d *dummyDirInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (d *dummyDirInfo) ModTime() time.Time { return time.Time{} }
func (d *dummyDirInfo) IsDir() bool        { return true }
func (d *dummyDirInfo) Sys() interface{}   { return nil }

func (d *singleDriver) Stat(ctx *server.Context, path string) (fs.FileInfo, error) {
	if path == "/" {
		return &dummyDirInfo{}, nil
	}
	return &dummyFileInfo{}, nil
}
func (d *singleDriver) ChangeDir(ctx *server.Context, path string) error { return nil }
func (d *singleDriver) ListDir(ctx *server.Context, path string, cb func(fs.FileInfo) error) error {
	return nil
}
func (d *singleDriver) DeleteDir(ctx *server.Context, path string) error  { return nil }
func (d *singleDriver) DeleteFile(ctx *server.Context, path string) error { return nil }
func (d *singleDriver) Rename(ctx *server.Context, from, to string) error { return nil }
func (d *singleDriver) MakeDir(ctx *server.Context, path string) error    { return nil }

type dummyReadCloser struct{}

func (r *dummyReadCloser) Read(p []byte) (int, error) { return 0, io.EOF }
func (r *dummyReadCloser) Close() error               { return nil }

func (d *singleDriver) GetFile(ctx *server.Context, path string, offset int64) (int64, io.ReadCloser, error) {
	return 0, &dummyReadCloser{}, nil
}
