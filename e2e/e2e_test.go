package e2e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	numLines   = 20000
	lineLength = 5000
)

func TestFollowStress(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "file_a.log")
	fileB := filepath.Join(dir, "file_b.log")
	output := filepath.Join(dir, "output.txt")

	for _, f := range []string{fileA, fileB} {
		if err := os.WriteFile(f, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	outFile, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	logf := func(format string, args ...any) {
		t.Logf("[%5.1fs] "+format, append([]any{time.Since(t0).Seconds()}, args...)...)
	}

	muxtail := exec.Command("go", "run", "..", "-f", "-n", "0",
		"--label=[A] ", "--label=[B] ", fileA, fileB)
	muxtail.Stdout = outFile
	muxtail.Stderr = os.Stderr
	// Put the process in its own group so we can kill go run + the compiled child together.
	muxtail.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logf("starting muxtail (go run)")
	if err := muxtail.Start(); err != nil {
		_ = outFile.Close()
		t.Fatal(err)
	}
	logf("muxtail pid %d started", muxtail.Process.Pid)
	// Register cleanup after t.TempDir() so it runs first (LIFO):
	// kill muxtail (releases its fd to output), close outFile, then TempDir removes the dir.
	t.Cleanup(func() {
		// Kill the entire process group to catch the compiled binary spawned by go run.
		_ = syscall.Kill(-muxtail.Process.Pid, syscall.SIGKILL)
		_ = muxtail.Wait()
		_ = outFile.Close()
	})

	// Give muxtail time to attach inotify watches.
	time.Sleep(200 * time.Millisecond)
	logf("sleep done, starting writers")

	// Start both writers simultaneously via a shared start signal.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	writeLines := func(path, prefix, char string) {
		defer wg.Done()
		<-start
		f, err := os.OpenFile(path, os.O_WRONLY, 0644)
		if err != nil {
			t.Errorf("open %s: %v", path, err)
			return
		}
		defer func() { _ = f.Close() }()
		payload := strings.Repeat(char, lineLength)
		w := bufio.NewWriterSize(f, 1<<20)
		for i := 0; i < numLines; i++ {
			_, _ = fmt.Fprintf(w, "%s:%09d:%s\n", prefix, i, payload)
		}
		if err := w.Flush(); err != nil {
			t.Errorf("flush %s: %v", path, err)
		}
	}

	go writeLines(fileA, "AAA", "X")
	go writeLines(fileB, "BBB", "Y")
	close(start) // release both goroutines at the same instant

	wg.Wait()
	logf("writers done, output so far: %d lines", countNewlines(output))

	// Poll until expected line count appears or timeout.
	// countNewlines streams through the file to avoid loading it into memory.
	expected := numLines * 2
	deadline := time.Now().Add(60 * time.Second)
	for {
		n := countNewlines(output)
		logf("poll: %d / %d lines", n, expected)
		if n >= expected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d / %d lines", countNewlines(output), expected)
		}
		time.Sleep(time.Second)
	}
	logf("poll complete")

	_ = syscall.Kill(-muxtail.Process.Pid, syscall.SIGKILL)
	_ = muxtail.Wait()

	// Stream through the output file once for all checks — no full load into memory.
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	payloadX := strings.Repeat("X", lineLength)
	payloadY := strings.Repeat("Y", lineLength)
	seqRe := regexp.MustCompile(`([AB]{3}):(\d{9}):`)
	seqA := make(map[string]int, numLines)
	seqB := make(map[string]int, numLines)
	lineCount, invalid, aWithBBB, bWithAAA := 0, 0, 0, 0

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// Check 2: pattern integrity — label prefix + 9-digit seq + payload of correct char/length.
		if !validLine(line, "[A] ", "AAA", payloadX) &&
			!validLine(line, "[B] ", "BBB", payloadY) {
			invalid++
			if invalid <= 5 {
				end := min(len(line), 80)
				t.Logf("invalid line: %q", line[:end])
			}
		}

		// Check 3: cross-contamination.
		if strings.HasPrefix(line, "[A]") && strings.Contains(line, "BBB") {
			aWithBBB++
		}
		if strings.HasPrefix(line, "[B]") && strings.Contains(line, "AAA") {
			bWithAAA++
		}

		// Check 4: sequence completeness.
		if m := seqRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "AAA":
				seqA[m[2]]++
			case "BBB":
				seqB[m[2]]++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	// Check 1: line count.
	if lineCount != expected {
		t.Errorf("check 1 line count: got %d, want %d", lineCount, expected)
	}

	if invalid > 0 {
		t.Errorf("check 2 pattern integrity: %d invalid lines", invalid)
	}

	if aWithBBB != 0 || bWithAAA != 0 {
		t.Errorf("check 3 cross-contamination: [A]+BBB=%d [B]+AAA=%d", aWithBBB, bWithAAA)
	}

	seqErrs := 0
	for i := 0; i < numLines; i++ {
		seq := fmt.Sprintf("%09d", i)
		if seqA[seq] != 1 {
			seqErrs++
			if seqErrs <= 5 {
				t.Logf("[A] seq %s count=%d", seq, seqA[seq])
			}
		}
		if seqB[seq] != 1 {
			seqErrs++
			if seqErrs <= 5 {
				t.Logf("[B] seq %s count=%d", seq, seqB[seq])
			}
		}
	}
	if seqErrs > 0 {
		t.Errorf("check 4 sequence completeness: %d missing/duplicate", seqErrs)
	}
}

// countNewlines streams through the file counting newlines without loading it into memory.
func countNewlines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 32*1024)
	count := 0
	for {
		n, err := f.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				count++
			}
		}
		if err != nil {
			break
		}
	}
	return count
}

// isNineDigits reports whether s is exactly 9 ASCII decimal digits.
func isNineDigits(s string) bool {
	if len(s) != 9 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validLine checks that line matches: <label><prefix>:<9-digit-seq>:<payload>
func validLine(line, label, prefix, payload string) bool {
	rest, ok := strings.CutPrefix(line, label+prefix+":")
	if !ok {
		return false
	}
	seq, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return false
	}
	return isNineDigits(seq) && rest == payload
}
