package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// fileChanges carries channels for file-system events on a watched file.
// Each channel is buffered (size 1); senders drop events when the receiver
// is not ready so the goroutine never blocks.
type fileChanges struct {
	modified <-chan struct{} // file was written to
	deleted  <-chan struct{} // file was removed or renamed
}

// watchFile registers path with an fsnotify watcher and returns a fileChanges
// whose channels fire when the file is written to or deleted. The background
// goroutine exits when ctx is cancelled.
func watchFile(ctx context.Context, path string) (*fileChanges, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(path); err != nil {
		w.Close()
		return nil, err
	}

	modifiedCh := make(chan struct{}, 1)
	deletedCh := make(chan struct{}, 1)

	go func() {
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-w.Events:
				if !ok {
					close(deletedCh)
					return
				}
				switch {
				case event.Has(fsnotify.Write):
					select {
					case modifiedCh <- struct{}{}:
					default:
					}
				case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
					select {
					case deletedCh <- struct{}{}:
					default:
					}
					return
				}
			case _, ok := <-w.Errors:
				if !ok {
					close(deletedCh)
					return
				}
			}
		}
	}()

	return &fileChanges{modified: modifiedCh, deleted: deletedCh}, nil
}

// waitUntilExists blocks until path exists on the filesystem or ctx is
// cancelled. Returns nil when the file appears, ctx.Err() when cancelled.
func waitUntilExists(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	if err := w.Add(dir); err != nil {
		return err
	}

	// Re-check after adding the watch to close the TOCTOU gap.
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-w.Events:
			if !ok {
				return errors.New("watcher closed unexpectedly")
			}
			if filepath.Base(event.Name) == base && event.Has(fsnotify.Create) {
				return nil
			}
		case _, ok := <-w.Errors:
			if !ok {
				return errors.New("watcher closed unexpectedly")
			}
		}
	}
}
