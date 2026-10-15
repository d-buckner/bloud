// SPDX-License-Identifier: AGPL-3.0-only

package appasset

import (
	"bytes"
	"io"
	"io/fs"
	"sync/atomic"
	"time"
)

func atomicInc(p *int32) int32  { return atomic.AddInt32(p, 1) }
func atomicLoad(p *int32) int32 { return atomic.LoadInt32(p) }

// singleFileFS is a tiny in-memory fs.FS with one file, for embed-source tests.
type singleFileFS struct {
	name    string
	content string
}

func (s singleFileFS) Open(n string) (fs.File, error) {
	if n != s.name {
		return nil, fs.ErrNotExist
	}
	return &singleFile{reader: bytes.NewReader([]byte(s.content))}, nil
}

type singleFile struct{ reader *bytes.Reader }

func (f *singleFile) Stat() (fs.FileInfo, error) { return fakeInfo{}, nil }
func (f *singleFile) Read(p []byte) (int, error) { return f.reader.Read(p) }
func (f *singleFile) Close() error               { return nil }

type fakeInfo struct{}

func (fakeInfo) Name() string       { return "file" }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() fs.FileMode  { return 0644 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() any           { return nil }

var _ io.Closer = (*singleFile)(nil)
