//go:build !windows

package main

import (
	"io/fs"
	"syscall"
	"testing"
	"time"
)

// fakeFileInfo implements os.FileInfo with a configurable Sys() return value.
type fakeFileInfo struct{ sys any }

func (f fakeFileInfo) Name() string      { return "fake" }
func (f fakeFileInfo) Size() int64       { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool       { return false }
func (f fakeFileInfo) Sys() any          { return f.sys }

func TestFileInode_ValidStat(t *testing.T) {
	st := &syscall.Stat_t{Ino: 42}
	if got := fileInode(fakeFileInfo{sys: st}); got != 42 {
		t.Fatalf("want 42, got %d", got)
	}
}

func TestFileInode_NonStatSys(t *testing.T) {
	// Sys() returns something that isn't *syscall.Stat_t — should return 0.
	if got := fileInode(fakeFileInfo{sys: nil}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}
