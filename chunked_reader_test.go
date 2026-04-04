package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestChunkedLineReader_SimpleLines(t *testing.T) {
	r := newChunkedLineReader(strings.NewReader("hello\nworld\n"), 32, 10<<20)

	line, truncated, err := r.ReadLine()
	if err != nil || truncated || line != "hello" {
		t.Fatalf("got (%q, %v, %v), want (\"hello\", false, nil)", line, truncated, err)
	}
	line, truncated, err = r.ReadLine()
	if err != nil || truncated || line != "world" {
		t.Fatalf("got (%q, %v, %v), want (\"world\", false, nil)", line, truncated, err)
	}
	_, _, err = r.ReadLine()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestChunkedLineReader_TruncatesLongLine(t *testing.T) {
	const maxLine = 100
	long := strings.Repeat("A", 300) + "\nshort\n"
	r := newChunkedLineReader(strings.NewReader(long), 32, maxLine)

	line, truncated, err := r.ReadLine()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated=true for long line")
	}
	if len(line) != maxLine {
		t.Fatalf("want %d bytes, got %d", maxLine, len(line))
	}
	if line != strings.Repeat("A", maxLine) {
		t.Fatalf("truncated content is wrong: %q", line)
	}

	line, truncated, err = r.ReadLine()
	if err != nil || truncated || line != "short" {
		t.Fatalf("after truncation: got (%q, %v, %v), want (\"short\", false, nil)", line, truncated, err)
	}
}

func TestChunkedLineReader_PartialLineReturnsEOF(t *testing.T) {
	r := newChunkedLineReader(strings.NewReader("incomplete"), 32, 10<<20)

	line, truncated, err := r.ReadLine()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got err=%v line=%q", err, line)
	}
	if line != "" || truncated {
		t.Fatalf("expected empty line on partial read, got %q truncated=%v", line, truncated)
	}
}

func TestChunkedLineReader_PartialLineThenCompletion(t *testing.T) {
	pr, pw := io.Pipe()
	r := newChunkedLineReader(pr, 32, 10<<20)

	go func() {
		pw.Write([]byte("hel"))
		pw.Write([]byte("lo\n"))
		pw.Close()
	}()

	var got string
	for i := 0; i < 100; i++ {
		line, _, err := r.ReadLine()
		if line != "" {
			got = line
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got != "hello" {
		t.Fatalf("want %q, got %q", "hello", got)
	}
}

func TestChunkedLineReader_EmptyLines(t *testing.T) {
	r := newChunkedLineReader(strings.NewReader("\n\nfoo\n"), 32, 10<<20)

	line, _, err := r.ReadLine()
	if err != nil || line != "" {
		t.Fatalf("want empty line, got (%q, %v)", line, err)
	}
	line, _, err = r.ReadLine()
	if err != nil || line != "" {
		t.Fatalf("want empty line, got (%q, %v)", line, err)
	}
	line, _, err = r.ReadLine()
	if err != nil || line != "foo" {
		t.Fatalf("want \"foo\", got (%q, %v)", line, err)
	}
}

func TestChunkedLineReader_ExactlyMaxLine(t *testing.T) {
	const maxLine = 10
	// Line is exactly maxLine bytes — should NOT be truncated.
	input := strings.Repeat("B", maxLine) + "\n"
	r := newChunkedLineReader(strings.NewReader(input), 4, maxLine)

	line, truncated, err := r.ReadLine()
	if err != nil || truncated || line != strings.Repeat("B", maxLine) {
		t.Fatalf("got (%q, truncated=%v, %v)", line, truncated, err)
	}
}

func TestChunkedLineReader_LargeLineSmallBuf(t *testing.T) {
	// buf smaller than a line — verifies multi-chunk accumulation works.
	const maxLine = 1000
	input := strings.Repeat("C", 500) + "\n"
	r := newChunkedLineReader(strings.NewReader(input), 16, maxLine)

	line, truncated, err := r.ReadLine()
	if err != nil || truncated || len(line) != 500 {
		t.Fatalf("got len=%d truncated=%v err=%v", len(line), truncated, err)
	}
}
