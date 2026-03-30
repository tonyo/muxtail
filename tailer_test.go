package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

// limitedReadSeeker wraps an io.ReadSeeker and returns an error if a single
// Read call requests more than maxRead bytes. Used to verify lastNLines does
// not allocate a large contiguous buffer for the forward pass.
type limitedReadSeeker struct {
	io.ReadSeeker
	maxRead int
}

func (l *limitedReadSeeker) Read(p []byte) (int, error) {
	if len(p) > l.maxRead {
		return 0, fmt.Errorf("Read called with buffer size %d > max %d", len(p), l.maxRead)
	}
	return l.ReadSeeker.Read(p)
}

func TestLastNLines_NoLargeForwardAlloc(t *testing.T) {
	// Verify the forward pass streams rather than allocating one large buffer.
	// maxRead == scannerInitBuf; tail of 100 long lines ≈ 70KB.
	// Old code: io.ReadFull(r, 70KB-buf) → Read(70KB) > maxRead → error.
	// New code: scanner reads in ≤scannerInitBuf chunks → each Read ≤ maxRead → ok.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "line %04d %s\n", i, strings.Repeat("x", 700))
	}
	r := &limitedReadSeeker{ReadSeeker: strings.NewReader(sb.String()), maxRead: scannerInitBuf}
	lines, err := lastNLines(r, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 100 {
		t.Fatalf("want 100 lines, got %d", len(lines))
	}
}

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

func TestLastNLines_LongLine(t *testing.T) {
	// Line longer than bufio.Scanner's default 64KB token limit.
	longLine := strings.Repeat("x", 200*1024) // 200 KB
	input := "before\n" + longLine + "\nafter\n"
	r := strings.NewReader(input)
	lines, err := lastNLines(r, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if lines[1] != longLine {
		t.Errorf("long line was truncated: got len %d, want %d", len(lines[1]), len(longLine))
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

func TestWriter_Timestamps(t *testing.T) {
	var buf bytes.Buffer
	w := &Writer{w: &buf, timestamps: true}
	w.WriteLine("[lbl] ", "hello")
	got := buf.String()
	matched, err := regexp.MatchString(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2}) \[lbl\] hello\n$`, got)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Errorf("output %q does not match timestamp pattern", got)
	}
}

func TestWriter_Timestamps_UsesNowFn(t *testing.T) {
	fixed := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	w := &Writer{w: &buf, timestamps: true, nowFn: func() time.Time { return fixed }}
	w.WriteLine("[lbl] ", "hello")
	got := buf.String()
	want := "2024-01-15T09:00:00Z [lbl] hello\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestColorizeLabel(t *testing.T) {
	got := colorizeLabel("[api] ", "\033[36m")
	want := "\033[36m[api] \033[0m"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if colorizeLabel("", "\033[36m") != "" {
		t.Error("empty label should be unchanged")
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
	if _, err := emitLastN(f.Name(), 5, "[x] ", w); err != nil {
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
	_, err := emitLastN("/nonexistent/path.log", 5, "[x] ", w)
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

	cap := &captureWriter{}
	w := cap.writer()
	spec := FileSpec{Path: name, Label: "[f] "}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = tailFile(ctx, spec, 0, true, false, w)
		close(done)
	}()

	// Give tailer time to start following.
	time.Sleep(100 * time.Millisecond)

	// Append lines to the file.
	f2, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o644)
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
		if len(cap.snapshot()) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done

	lines := cap.snapshot()
	for i := 1; i <= 3; i++ {
		want := fmt.Sprintf("[f] new line %d", i)
		found := false
		for _, l := range lines {
			if strings.Contains(l, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in output: %v", want, lines)
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
		_ = tailFile(ctx, spec, 3, false, false, w)
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

func TestTailFile_FollowMissingFile(t *testing.T) {
	spec := FileSpec{Path: "/nonexistent/muxtail_missing.log", Label: "[m] "}
	var buf bytes.Buffer
	w := &Writer{w: &buf}
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, spec, 0, true, false, w)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error for missing file with follow=true, retry=false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tailFile did not return promptly for missing file with follow=true, retry=false")
	}
}

func TestTailFile_FollowRetry_FirstWriteVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	spec := FileSpec{Path: path, Label: "[r] "}
	cap := &captureWriter{}
	w := cap.writer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, spec, 0, true, true, w)
	}()

	// Give tailer time to start watching.
	time.Sleep(100 * time.Millisecond)

	// Create the file and write the first line.
	if err := os.WriteFile(path, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for the first line to appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lines := cap.snapshot(); len(lines) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done

	lines := cap.snapshot()
	if len(lines) == 0 {
		t.Fatal("first write to newly-created file was not captured")
	}
	if lines[0] != "[r] first line" {
		t.Errorf("got %q, want %q", lines[0], "[r] first line")
	}
}

func TestTailFile_FollowRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	spec := FileSpec{Path: path, Label: "[r] "}
	var buf bytes.Buffer
	w := &Writer{w: &buf}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, spec, 0, true, true, w)
	}()

	// Should NOT return immediately — it blocks watching for the file.
	select {
	case <-done:
		t.Fatal("tailFile with follow=true, retry=true returned before ctx cancel")
	case <-time.After(200 * time.Millisecond):
		// expected: still running
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailFile did not return after ctx cancel")
	}
}

func TestTailFile_FollowRetry_NoStderrOnMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	spec := FileSpec{Path: path, Label: "[r] "}
	var buf bytes.Buffer
	w := &Writer{w: &buf}
	ctx, cancel := context.WithCancel(context.Background())

	// Redirect os.Stderr to capture any spurious output.
	r, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = wPipe

	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, spec, 0, true, true, w)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	wPipe.Close()
	os.Stderr = origStderr

	var stderrBuf bytes.Buffer
	io.Copy(&stderrBuf, r)
	r.Close()

	if stderrBuf.Len() > 0 {
		t.Errorf("unexpected stderr output with -F and missing file: %q", stderrBuf.String())
	}
}

func TestTailFile_FollowDoesNotMissLinesBetweenEmitAndFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Write 5 initial lines — emitLastN will read exactly these 5 and stop.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		fmt.Fprintf(f, "initial %d\n", i)
	}
	f.Close()

	// Append 3 "race window" lines to the file before the tailer starts.
	// emitLastN is called with n=5, so it reads up to the offset after "initial 4\n"
	// and then closes the file. nxadm/tail then opens the file independently.
	// Bug:  nxadm/tail seeks to the current EOF (after these 3 lines) — skipping them.
	// Fix:  nxadm/tail seeks to emitLastN's stop offset — picks them up.
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fmt.Fprintf(f, "racewindow %d\n", i)
	}
	f.Close()

	cap := &captureWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		_ = tailFile(ctx, FileSpec{Path: path, Label: ""}, 5, true, false, cap.writer())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var found int
		for _, l := range cap.snapshot() {
			if strings.HasPrefix(l, "racewindow ") {
				found++
			}
		}
		if found == 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected all 3 race-window lines to appear in follow output, but they were skipped")
}

func TestTailFile_FollowWithInitialLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()

	cap := &captureWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = tailFile(ctx, FileSpec{Path: path, Label: ""}, 5, true, false, cap.writer())
	}()

	// Wait for the 5 initial lines (lines 6-10).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	initial := cap.snapshot()
	if len(initial) != 5 {
		t.Fatalf("want 5 initial lines, got %d: %v", len(initial), initial)
	}
	for i, want := range []string{"line 6", "line 7", "line 8", "line 9", "line 10"} {
		if initial[i] != want {
			t.Errorf("initial[%d]: want %q, got %q", i, want, initial[i])
		}
	}

	// Append 3 new lines and verify they're followed.
	f2, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 11; i <= 13; i++ {
		fmt.Fprintf(f2, "line %d\n", i)
	}
	f2.Close()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) >= 8 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	all := cap.snapshot()
	if len(all) != 8 {
		t.Fatalf("want 8 total lines (5 initial + 3 new), got %d: %v", len(all), all)
	}
	for i, want := range []string{"line 11", "line 12", "line 13"} {
		if all[5+i] != want {
			t.Errorf("new[%d]: want %q, got %q", i, want, all[5+i])
		}
	}
}

func TestEmitLastN_ZeroNReturnsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	content := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	w := &Writer{w: &buf}
	offset, err := emitLastN(path, 0, "", w)
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("n=0: expected no output, got %q", buf.String())
	}
	want := int64(len(content))
	if offset != want {
		t.Errorf("n=0: offset = %d, want %d", offset, want)
	}
}

func TestEmitLastN_ReturnsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	content := sb.String()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	w := &Writer{w: &buf}
	offset, err := emitLastN(path, 3, "", w)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(content))
	if offset != want {
		t.Errorf("offset = %d, want %d (file size)", offset, want)
	}
}

// --- resolveLabel ---

func TestResolveLabel(t *testing.T) {
	cases := []struct {
		path, mode, want string
	}{
		{"app.log", "none", ""},
		{"app.log", "basename", "app.log:"},
		{"-", "basename", "stdin:"},
		{"-", "abspath", "stdin:"},
		{"app.log", "", ""},
	}
	for _, tc := range cases {
		got := resolveLabel(tc.path, tc.mode)
		if got != tc.want {
			t.Errorf("resolveLabel(%q, %q) = %q, want %q", tc.path, tc.mode, got, tc.want)
		}
	}

	// abspath resolves to absolute path regardless of how the path was given.
	t.Run("abspath resolves relative", func(t *testing.T) {
		got := resolveLabel("app.log", "abspath")
		if !strings.HasPrefix(got, "/") || !strings.HasSuffix(got, "/app.log:") {
			t.Errorf("resolveLabel(app.log, abspath) = %q, want absolute path ending in /app.log:", got)
		}
	})
	t.Run("abspath keeps absolute", func(t *testing.T) {
		got := resolveLabel("/a/b.log", "abspath")
		if got != "/a/b.log:" {
			t.Errorf("resolveLabel(/a/b.log, abspath) = %q, want /a/b.log:", got)
		}
	})
}

// --- tailStdin ---

func TestTailStdin(t *testing.T) {
	input := "line one\nline two\nline three\n"
	r := strings.NewReader(input)

	cap := &captureWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = tailStdin(ctx, r, "[in] ", cap.writer())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailStdin did not return after reader was exhausted")
	}

	want := []string{"[in] line one", "[in] line two", "[in] line three"}
	got := cap.snapshot()
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d: want %q, got %q", i, w, got[i])
		}
	}
}

func TestTailStdin_CancelMidStream(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	cap := &captureWriter{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = tailStdin(ctx, pr, "[in] ", cap.writer())
		close(done)
	}()

	fmt.Fprintln(pw, "hello")

	// Wait for the line to arrive then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailStdin did not return after context cancel")
	}

	lines := cap.snapshot()
	if len(lines) == 0 || lines[0] != "[in] hello" {
		t.Errorf("want [\"[in] hello\"], got %v", lines)
	}
}

// --- buildSpecs ---

func TestTailStdin_LongLine(t *testing.T) {
	longLine := strings.Repeat("y", 200*1024) // 200 KB
	input := longLine + "\n"
	r := strings.NewReader(input)

	cap := &captureWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = tailStdin(ctx, r, "", cap.writer())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailStdin did not return after reader exhausted")
	}

	lines := cap.snapshot()
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if lines[0] != longLine {
		t.Errorf("long line was truncated: got len %d, want %d", len(lines[0]), len(longLine))
	}
}

// --- buildSpecs ---

func TestBuildSpecs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		labels     []string
		prefixMode string
		wantSpecs  []FileSpec
		wantErr    bool
	}{
		{
			name:       "single file no prefix",
			args:       []string{"f1"},
			prefixMode: "none",
			wantSpecs:  []FileSpec{{Path: "f1", Label: ""}},
		},
		{
			name:       "basename prefix two files",
			args:       []string{"f1", "f2"},
			prefixMode: "basename",
			wantSpecs:  []FileSpec{{Path: "f1", Label: "f1:"}, {Path: "f2", Label: "f2:"}},
		},
		{
			name:       "abspath prefix",
			args:       []string{"/a/b.log"},
			prefixMode: "abspath",
			wantSpecs:  []FileSpec{{Path: "/a/b.log", Label: "/a/b.log:"}},
		},
		{
			name:       "label overrides first file, prefix applies to second",
			args:       []string{"f1", "f2"},
			labels:     []string{"[A] "},
			prefixMode: "basename",
			wantSpecs:  []FileSpec{{Path: "f1", Label: "[A] "}, {Path: "f2", Label: "f2:"}},
		},
		{
			name:       "labels for all files",
			args:       []string{"f1", "f2"},
			labels:     []string{"[A] ", "[B] "},
			prefixMode: "none",
			wantSpecs:  []FileSpec{{Path: "f1", Label: "[A] "}, {Path: "f2", Label: "[B] "}},
		},
		{
			name:    "more labels than files",
			args:    []string{"f1"},
			labels:  []string{"[A] ", "[B] "},
			wantErr: true,
		},
		{
			name:       "no args stdin",
			args:       []string{"-"},
			prefixMode: "none",
			wantSpecs:  []FileSpec{{Path: "-", Label: ""}},
		},
		{
			name:       "stdin with basename prefix",
			args:       []string{"-"},
			prefixMode: "basename",
			wantSpecs:  []FileSpec{{Path: "-", Label: "stdin:"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := buildSpecs(tc.args, tc.labels, tc.prefixMode)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(specs) != len(tc.wantSpecs) {
				t.Fatalf("specs len: want %d, got %d: %v", len(tc.wantSpecs), len(specs), specs)
			}
			for i, s := range tc.wantSpecs {
				if specs[i].Path != s.Path || specs[i].Label != s.Label {
					t.Errorf("spec[%d]: want %+v, got %+v", i, s, specs[i])
				}
			}
		})
	}
}

// --- emitLastN with label prefix ---

func TestEmitLastN_WithLabel(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.CreateTemp(dir, "app.log")
	f.Close()
	os.WriteFile(f.Name(), []byte("hello\n"), 0o644)

	var buf bytes.Buffer
	w := &Writer{w: &buf}
	if _, err := emitLastN(f.Name(), 1, "app.log:", w); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	if got != "app.log:hello" {
		t.Errorf("want %q, got %q", "app.log:hello", got)
	}
}

// --- multi-error ---

func TestTailFile_MultipleErrorsAllReported(t *testing.T) {
	dir := t.TempDir()
	missing1 := filepath.Join(dir, "no_such_1.log")
	missing2 := filepath.Join(dir, "no_such_2.log")

	specs := []FileSpec{
		{Path: missing1, Label: "[1] "},
		{Path: missing2, Label: "[2] "},
	}

	ctx := context.Background()
	errCh := make(chan error, len(specs))
	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- tailFile(ctx, spec, 10, true, false, &Writer{w: io.Discard})
		}()
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 2 {
		t.Fatalf("want 2 errors, got %d: %v", len(errs), errs)
	}
	for i, err := range errs {
		if !strings.Contains(err.Error(), "no_such_") {
			t.Errorf("error %d doesn't mention the missing file: %v", i, err)
		}
	}
}

// --- flagLines validation ---

func TestRun_NegativeLines_ReturnsError(t *testing.T) {
	orig := flagLines
	flagLines = -5
	defer func() { flagLines = orig }()

	err := run(rootCmd, []string{"/dev/null"})
	if err == nil {
		t.Fatal("expected error for --lines=-5, got nil")
	}
	if !strings.Contains(err.Error(), "lines") {
		t.Errorf("error should mention 'lines', got: %v", err)
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
