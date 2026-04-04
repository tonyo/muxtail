package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tomb "gopkg.in/tomb.v1"
)

// tailFile tails a regular file: first emit last N lines, then follow if follow==true.
// When follow=true and retry=false (-f), the file must exist at startup or an error is returned.
// When follow=true and retry=true (-F), missing files are tolerated and watched until they appear.

const (
	// scannerInitBuf is the initial buffer size for line scanners.
	scannerInitBuf = 64 * 1024
	// scannerMaxBuf is the maximum line size a scanner will accept.
	// Lines longer than this are treated as errors.
	scannerMaxBuf = 1024 * 1024

	// defaultMaxLineBytes is the default cap for per-line memory in the follow phase.
	defaultMaxLineBytes = 10 * 1024 * 1024

	// followReadBufSize is the size of the fixed read buffer used by chunkedLineReader
	// in the follow phase.
	followReadBufSize = 32 * 1024
)

// tailOptions configures optional behaviour for tailFileWithOptions.
type tailOptions struct {
	// maxLineBytes caps the memory used per line in the follow phase.
	// 0 means use defaultMaxLineBytes.
	maxLineBytes int
}

// tailFile is the public entry point; it calls tailFileWithOptions with defaults.
func tailFile(ctx context.Context, spec FileSpec, n int, follow, retry bool, w *Writer) error {
	return tailFileWithOptions(ctx, spec, n, follow, retry, w, tailOptions{})
}

func tailFileWithOptions(ctx context.Context, spec FileSpec, n int, follow, retry bool, w *Writer, opts tailOptions) error {
	if follow && !retry {
		if _, err := os.Stat(spec.Path); err != nil {
			return fmt.Errorf("%s: %w", spec.Path, err)
		}
	}

	// Record the file's inode and size before emitLastN so we can detect
	// truncation or rotation that occurs between emitLastN closing the file
	// and the follow phase opening it.
	maxLine := opts.maxLineBytes
	if maxLine <= 0 {
		maxLine = defaultMaxLineBytes
	}

	var inode1 uint64
	if fi, err := os.Stat(spec.Path); err == nil {
		inode1 = fileInode(fi)
	}

	emitOffset, emitErr := emitLastN(spec.Path, n, spec.Label, w, maxLine)
	if emitErr != nil && (!retry || !os.IsNotExist(emitErr)) {
		w.WriteError(fmt.Sprintf("muxtail: %s: %v\n", spec.Path, emitErr))
	}

	if !follow {
		return nil
	}

	// Determine where the follow phase should start reading.
	//
	// Default: absolute byte offset recorded by emitLastN (avoids missing lines
	// written between emitLastN closing the file and the follow phase opening it).
	//
	// Adjustments based on what happened while emitLastN was running:
	//   - Different inode: file was replaced (log rotation) → start at 0
	//   - Same inode, smaller size: truncated in-place → start at new EOF
	//   - File doesn't exist yet (retry mode): start at 0
	seekOffset := emitOffset
	if retry {
		if _, statErr := os.Stat(spec.Path); os.IsNotExist(statErr) {
			seekOffset = 0
		}
	} else if fi, err := os.Stat(spec.Path); err == nil {
		switch inode2 := fileInode(fi); {
		case inode2 != inode1:
			seekOffset = 0 // file was replaced
		case fi.Size() < emitOffset:
			seekOffset = fi.Size() // truncated in-place
		}
	}

	return followWithChunkedReader(
		ctx,
		spec.Path,
		seekOffset,
		retry,
		maxLine,
		func(line string) error {
			return w.WriteLine(spec.Label, line)
		},
		func(msg string) {
			w.WriteError(msg)
		},
	)
}

// followWithChunkedReader tails path starting at seekOffset using a chunkedLineReader.
// It uses the nxadm/tail watch package for inotify/polling events and reads the file
// directly, capping per-line memory at maxLineBytes. Lines exceeding the cap are
// truncated: onLine is called with truncated=true, then onError is called with a
// warning message. Callers should use onError to forward warnings to the user.
//
// followWithChunkedReader handles log rotation (file replaced: Deleted event) and
// in-place truncation (Truncated event) by reopening the file and resetting the reader.
//
// Context cancellation causes the function to return nil promptly.
func followWithChunkedReader(
	ctx context.Context,
	path string,
	seekOffset int64,
	retry bool,
	maxLineBytes int,
	onLine func(line string) error,
	onError func(msg string),
) error {
	var t tomb.Tomb
	defer t.Kill(nil) // unblocks bridge goroutine on any return path
	go func() {
		select {
		case <-ctx.Done():
			t.Kill(nil)
		case <-t.Dead():
		}
	}()

	for {
		err := followOnce(ctx, &t, path, seekOffset, retry, maxLineBytes, onLine, onError)
		if errors.Is(err, errRetryOpen) {
			// File was deleted; retry mode: wait for it to reappear.
			seekOffset = 0
			watcher := newFileWatcher(path)
			if waitErr := watcher.BlockUntilExists(&t); waitErr != nil {
				// tomb was killed (ctx cancelled or other death).
				return nil
			}
			continue
		}
		return err
	}
}

// errRetryOpen is a sentinel returned by followOnce when the file was deleted
// and the caller should wait for it to reappear (retry mode).
var errRetryOpen = errors.New("file deleted, retry")

// followOnce runs a single follow session on path from seekOffset.
// Returns errRetryOpen when the file is deleted and retry==true.
// Returns nil when ctx is cancelled or follow==false-deleted.
func followOnce(
	ctx context.Context,
	t *tomb.Tomb,
	path string,
	seekOffset int64,
	retry bool,
	maxLineBytes int,
	onLine func(line string) error,
	onError func(msg string),
) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && retry {
			return errRetryOpen
		}
		return err
	}
	defer f.Close()

	if _, err := f.Seek(seekOffset, io.SeekStart); err != nil {
		return err
	}

	watcher := newFileWatcher(path)
	changes, err := watcher.ChangeEvents(t, seekOffset)
	if err != nil {
		return nil // tomb was killed
	}

	reader := newChunkedLineReader(f, followReadBufSize, maxLineBytes)

	for {
		line, truncated, readErr := reader.ReadLine()
		if readErr == nil {
			if truncated {
				onError(fmt.Sprintf("muxtail: %s: line truncated at %d bytes\n", path, maxLineBytes))
			}
			if err := onLine(line); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return readErr
		}

		// No complete line yet — wait for a file-change event.
		select {
		case <-ctx.Done():
			return nil
		case <-t.Dying():
			return nil
		case _, ok := <-changes.Modified:
			if !ok {
				return nil
			}
		case _, ok := <-changes.Truncated:
			if !ok {
				return nil
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			reader = newChunkedLineReader(f, followReadBufSize, maxLineBytes)
		case _, ok := <-changes.Deleted:
			if !ok {
				return nil
			}
			if retry {
				return errRetryOpen
			}
			return nil
		}
	}
}

// emitLastN reads the last n lines of a file and writes them.
// It returns the byte offset at which it stopped reading (the file size at open
// time), so the caller can resume following from exactly that position.
func emitLastN(path string, n int, label string, w *Writer, maxLine int) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	if n <= 0 {
		return size, nil
	}

	lines, err := lastNLines(f, n, maxLine)
	if err != nil {
		return 0, err
	}
	for _, line := range lines {
		if err := w.WriteLine(label, line); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// lastNLines returns up to n lines from the end of r.
func lastNLines(r io.ReadSeeker, n int, maxLine int) ([]string, error) {
	const chunkSize = 4096

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}

	// Peek at the last byte: if the file doesn't end with '\n', EOF acts as
	// a line terminator, so we need one fewer '\n' to delimit n lines.
	lastByte := make([]byte, 1)
	if _, err := r.Seek(-1, io.SeekEnd); err != nil {
		return nil, err
	}
	if _, err := r.Read(lastByte); err != nil {
		return nil, err
	}
	trailingNewline := lastByte[0] == '\n'

	// We want n lines, which means n newline boundaries. We stop when we've
	// found n+1 newline boundaries (or hit the start of file).
	// If there's no trailing newline, EOF counts as one boundary already.
	target := n + 1
	if !trailingNewline {
		target--
	}

	// Scan backwards to find the byte offset where the last n lines begin.
	// Reuse a single chunk buffer — no accumulated prepending.
	chunk := make([]byte, chunkSize)
	offset := size
	newlines := 0
	startAbs := int64(0)
outer:
	for offset > 0 {
		read := int64(chunkSize)
		if read > offset {
			read = offset
		}
		offset -= read
		if _, err := r.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, chunk[:read]); err != nil {
			return nil, err
		}
		for i := int(read) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				newlines++
				if newlines >= target {
					startAbs = offset + int64(i) + 1
					break outer
				}
			}
		}
	}

	// Seek to startAbs and stream the tail forward through a scanner —
	// avoids allocating a single buffer proportional to (size - startAbs).
	if _, err := r.Seek(startAbs, io.SeekStart); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, min(scannerInitBuf, maxLine)), maxLine)
	lines := make([]string, 0, min(n, 128))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// tailStdin reads lines from stdin and writes them with the given label.
//
// Known limitation: if the underlying reader (r) is a blocking pipe that
// never closes (e.g. a terminal or a long-lived process), cancelling ctx
// causes tailStdin to return but the scanner goroutine remains blocked
// inside scanner.Scan(). The goroutine will only be released when r is
// closed externally (e.g. the pipe writer exits or the OS closes stdin on
// process exit). This is a fundamental Go limitation with blocking I/O —
// there is no way to unblock a Read on a pipe without closing the fd.
// For the typical muxtail use-case (SIGINT/SIGTERM), stdin is closed as
// part of process shutdown so the leak is short-lived and acceptable.
func tailStdin(ctx context.Context, r io.Reader, label string, w *Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, scannerInitBuf), scannerMaxBuf)
	lines := make(chan string)
	done := make(chan struct{})

	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return scanner.Err()
			}
			if err := w.WriteLine(label, line); err != nil {
				return err
			}
		}
	}
}
