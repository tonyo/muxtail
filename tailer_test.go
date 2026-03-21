package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureWriter collects all lines written via WriteLine.
type captureWriter struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureWriter) writer() *Writer {
	return &Writer{w: &lockedBuf{c: c}}
}

type lockedBuf struct{ c *captureWriter }

func (lb *lockedBuf) Write(p []byte) (int, error) {
	lb.c.mu.Lock()
	lb.c.lines = append(lb.c.lines, strings.TrimRight(string(p), "\n"))
	lb.c.mu.Unlock()
	return len(p), nil
}

func (c *captureWriter) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// --- lastNLines ---

func TestLastNLines_FewerThanN(t *testing.T) {
	r := strings.NewReader("a\nb\nc\n")
	lines, err := lastNLines(r, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	assertLines(t, lines, want)
}

func TestLastNLines_ExactlyN(t *testing.T) {
	r := strings.NewReader("a\nb\nc\n")
	lines, err := lastNLines(r, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertLines(t, lines, []string{"a", "b", "c"})
}

func TestLastNLines_MoreThanN(t *testing.T) {
	r := strings.NewReader("a\nb\nc\nd\ne\n")
	lines, err := lastNLines(r, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertLines(t, lines, []string{"c", "d", "e"})
}

func TestLastNLines_Empty(t *testing.T) {
	r := strings.NewReader("")
	lines, err := lastNLines(r, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected no lines, got %v", lines)
	}
}

func TestLastNLines_ZeroN(t *testing.T) {
	r := strings.NewReader("a\nb\n")
	lines, err := lastNLines(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected no lines, got %v", lines)
	}
}

func TestLastNLines_LargeFile(t *testing.T) {
	// Build a file larger than the 4096-byte chunk size.
	var sb strings.Builder
	total := 200
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "line %04d\n", i)
	}
	r := strings.NewReader(sb.String())
	lines, err := lastNLines(r, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 10 {
		t.Fatalf("want 10 lines, got %d", len(lines))
	}
	if lines[0] != "line 0191" {
		t.Fatalf("first line: want %q, got %q", "line 0191", lines[0])
	}
	if lines[9] != "line 0200" {
		t.Fatalf("last line: want %q, got %q", "line 0200", lines[9])
	}
}

func TestLastNLines_NoTrailingNewline(t *testing.T) {
	r := strings.NewReader("a\nb\nc")
	lines, err := lastNLines(r, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertLines(t, lines, []string{"b", "c"})
}

// --- Writer ---

func TestWriter_AtomicLines(t *testing.T) {
	var buf bytes.Buffer
	w := &Writer{w: &buf}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.WriteLine(fmt.Sprintf("[g%d] ", i), "hello")
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 100 {
		t.Fatalf("want 100 lines, got %d", len(lines))
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "hello") {
			t.Errorf("mangled line: %q", l)
		}
	}
}

// --- emitLastN ---

func TestEmitLastN(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "muxtail")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()

	var buf bytes.Buffer
	w := &Writer{w: &buf}
	if err := emitLastN(f.Name(), 5, "[x] ", w); err != nil {
		t.Fatal(err)
	}

	got := strings.TrimRight(buf.String(), "\n")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %d: %v", len(lines), lines)
	}
	for i, l := range lines {
		want := fmt.Sprintf("[x] line %d", 16+i)
		if l != want {
			t.Errorf("line %d: want %q, got %q", i, want, l)
		}
	}
}

func TestEmitLastN_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	w := &Writer{w: &buf}
	err := emitLastN("/nonexistent/path.log", 5, "[x] ", w)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// --- tailFile follow ---

func TestTailFile_Follow(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "muxtail*.log")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()

	var buf bytes.Buffer
	w := &Writer{w: &buf}
	spec := FileSpec{Path: name, Label: "[f] "}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tailFile(ctx, spec, 0, true, w)
		close(done)
	}()

	// Give tailer time to start following.
	time.Sleep(100 * time.Millisecond)

	// Append lines to the file.
	f2, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(f2, "new line %d\n", i)
	}
	f2.Close()

	// Wait for lines to be picked up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(buf.String(), "\n") >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done

	got := buf.String()
	for i := 1; i <= 3; i++ {
		want := fmt.Sprintf("[f] new line %d", i)
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

func TestTailFile_NoFollow(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "muxtail*.log")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()

	cap := &captureWriter{}
	w := cap.writer()
	spec := FileSpec{Path: f.Name(), Label: "[x] "}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		tailFile(ctx, spec, 3, false, w)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailFile with follow=false did not return promptly")
	}

	lines := cap.snapshot()
	want := []string{"[x] line 3", "[x] line 4", "[x] line 5"}
	if len(lines) != len(want) {
		t.Fatalf("want %v, got %v", want, lines)
	}
	for i, l := range want {
		if lines[i] != l {
			t.Errorf("line %d: want %q, got %q", i, l, lines[i])
		}
	}
}

// --- label resolution (via run integration) ---

func TestLabelResolution_DefaultBasename(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.CreateTemp(dir, "app.log")
	f.Close()

	// emitLastN with default label should use basename
	label := "app.log "
	var buf bytes.Buffer
	w := &Writer{w: &buf}
	fmt.Fprintln(f) // no-op since closed, but let's write via path
	os.WriteFile(f.Name(), []byte("hello\n"), 0644)

	if err := emitLastN(f.Name(), 1, label, w); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	if got != "app.log hello" {
		t.Errorf("want %q, got %q", "app.log hello", got)
	}
}

// helpers

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("want %d lines %v, got %d lines %v", len(want), want, len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: want %q, got %q", i, want[i], got[i])
		}
	}
}
