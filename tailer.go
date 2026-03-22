package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/hpcloud/tail"
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

	if err := emitLastN(spec.Path, n, spec.Label, w); err != nil {
		fmt.Fprintf(os.Stderr, "muxtail: %s: %v\n", spec.Path, err)
	}

	if !follow {
		return nil
	}

	// If the file doesn't exist yet (retry mode), start from the beginning when
	// it appears. If it already exists, start from the end to skip old content.
	seekWhence := io.SeekEnd
	if retry {
		if _, statErr := os.Stat(spec.Path); os.IsNotExist(statErr) {
			seekWhence = io.SeekStart
		}
	}

	t, err := tail.TailFile(spec.Path, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: !retry,
		Location:  &tail.SeekInfo{Offset: 0, Whence: seekWhence},
		Logger:    tail.DiscardingLogger,
	})
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			_ = t.Stop()
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
func emitLastN(path string, n int, label string, w *Writer) error {
	if n <= 0 {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	lines, err := lastNLines(f, n)
	if err != nil {
		return err
	}
	for _, line := range lines {
		w.WriteLine(label, line)
	}
	return nil
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
