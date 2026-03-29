package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/nxadm/tail"
)

// tailFile tails a regular file: first emit last N lines, then follow if follow==true.
// When follow=true and retry=false (-f), the file must exist at startup or an error is returned.
// When follow=true and retry=true (-F), missing files are tolerated and watched until they appear.
func tailFile(ctx context.Context, spec FileSpec, n int, follow, retry bool, w *Writer) error {
	if follow && !retry {
		if _, err := os.Stat(spec.Path); err != nil {
			return fmt.Errorf("%s: %w", spec.Path, err)
		}
	}

	// Record the file's inode and size before emitLastN so we can detect
	// truncation or rotation that occurs between emitLastN closing the file
	// and nxadm/tail opening it.
	var inode1 uint64
	if fi, err := os.Stat(spec.Path); err == nil {
		inode1 = fileInode(fi)
	}

	emitOffset, emitErr := emitLastN(spec.Path, n, spec.Label, w)
	if emitErr != nil {
		fmt.Fprintf(os.Stderr, "muxtail: %s: %v\n", spec.Path, emitErr)
	}

	if !follow {
		return nil
	}

	// Determine where nxadm/tail should start reading.
	//
	// Default: absolute byte offset recorded by emitLastN (avoids missing lines
	// written between emitLastN closing the file and nxadm/tail opening it).
	//
	// Adjustments based on what happened while emitLastN was running:
	//   - Different inode: file was replaced (log rotation) → start at 0
	//   - Same inode, smaller size: truncated in-place → start at new EOF
	//   - File doesn't exist yet (retry mode): start at 0
	seekOffset := emitOffset
	seekWhence := io.SeekStart
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

	t, err := tail.TailFile(spec.Path, tail.Config{
		Follow:        true,
		ReOpen:        true,
		MustExist:     !retry,
		Location:      &tail.SeekInfo{Offset: seekOffset, Whence: seekWhence},
		Logger:        tail.DiscardingLogger,
		CompleteLines: true,
	})
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			t.Stop()
			t.Cleanup()
			return nil
		case line, ok := <-t.Lines:
			if !ok {
				return nil
			}
			if line.Err != nil {
				fmt.Fprintf(os.Stderr, "muxtail: %s: %v\n", spec.Path, line.Err)
				continue
			}
			w.WriteLine(spec.Label, line.Text)
		}
	}
}

// emitLastN reads the last n lines of a file and writes them.
// It returns the byte offset at which it stopped reading (the file size at open
// time), so the caller can resume following from exactly that position.
func emitLastN(path string, n int, label string, w *Writer) (int64, error) {
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

	lines, err := lastNLines(f, n)
	if err != nil {
		return 0, err
	}
	for _, line := range lines {
		w.WriteLine(label, line)
	}
	return size, nil
}

// lastNLines returns up to n lines from the end of r.
func lastNLines(r io.ReadSeeker, n int) ([]string, error) {
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

	// Seek to startAbs and read the tail in one forward pass.
	if _, err := r.Seek(startAbs, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, size-startAbs)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	lines := make([]string, 0, 128)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// tailStdin reads lines from stdin and writes them with the given label.
func tailStdin(ctx context.Context, r io.Reader, label string, w *Writer) error {
	scanner := bufio.NewScanner(r)
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
			w.WriteLine(label, line)
		}
	}
}
