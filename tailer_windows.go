//go:build windows

package main

import (
	"os"

	"github.com/nxadm/tail/watch"
)

// fileInode returns 0 on Windows where inode numbers are not available.
// Rotation detection via inode is skipped; size-based truncation detection
// and the absolute-offset race fix still apply.
func fileInode(_ os.FileInfo) uint64 {
	return 0
}

func newFileWatcher(path string) watch.FileWatcher {
	return watch.NewPollingFileWatcher(path)
}
