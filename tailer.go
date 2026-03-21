package main

import (
	"bufio"
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
		// non-fatal: file may be empty or unreadable for initial lines
		_ = err
	}

	if !follow {
		return nil
	}

	t, err := tail.TailFile(spec.Path, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: !retry,
		Location:  &tail.SeekInfo{Offset: 0, Whence: io.SeekEnd},
		Logger:    tail.DiscardingLogger,
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
	defer f.Close()

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

	// Scan backwards collecting newlines until we find n+1 of them.
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

	buf := make([]byte, 0, chunkSize)
	offset := size
	newlines := 0
	// We want n lines, which means n newline boundaries. We stop when we've
	// found n+1 newline boundaries (or hit the start of file).
	// If there's no trailing newline, EOF counts as one boundary already.
	target := n + 1
	if !trailingNewline {
		target--
	}

outer:
	for offset > 0 {
		read := int64(chunkSize)
		if read > offset {
			read = offset
		}
		offset -= read
		chunk := make([]byte, read)
		if _, err := r.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, err
		}
		// prepend chunk to buf
		buf = append(chunk, buf...)
		for i := int(read) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				newlines++
				if newlines >= target {
					// trim buf to start after this newline position
					pos := int64(i) + 1 // position in chunk
					absPos := offset + pos
					buf = buf[absPos-offset:]
					break outer
				}
			}
		}
	}

	// buf now contains the last n lines (possibly with trailing newline)
	scanner := bufio.NewScanner(newlineReader(buf))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

type bytesReader struct {
	data []byte
	pos  int
}

func newlineReader(data []byte) io.Reader {
	return &bytesReader{data: data}
}

func (b *bytesReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}

// tailStdin reads lines from stdin and writes them with the given label.
func tailStdin(ctx context.Context, label string, w *Writer) {
	scanner := bufio.NewScanner(os.Stdin)
	lines := make(chan string)

	go func() {
		defer close(lines)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			w.WriteLine(label, line)
		}
	}
}
