//go:build !windows

package main

import (
	"os"
	"syscall"

	"github.com/nxadm/tail/watch"
)

func fileInode(fi os.FileInfo) uint64 {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}

func newFileWatcher(path string) watch.FileWatcher {
	return watch.NewInotifyFileWatcher(path)
}
