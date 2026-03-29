package e2e

import (
	"bufio"
	"context"
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
	numLines   = 20002
	lineLength = 5000
)

func TestFollowStress(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "file_a.log")
	fileB := filepath.Join(dir, "file_b.log")
	output := filepath.Join(dir, "output.txt")

	for _, f := range []string{fileA, fileB} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
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

	// Build first so compilation time doesn't race with the writers.
	bin := filepath.Join(dir, "muxtail")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "..")
	build.Stderr = os.Stderr
	logf("building muxtail")
	if err := build.Run(); err != nil {
		_ = outFile.Close()
		t.Fatal("build failed:", err)
	}
	logf("build done")

	muxtail := exec.CommandContext(context.Background(), bin, "-f", "-n", "0",
		"--label=[A] ", "--label=[B] ", fileA, fileB)
	muxtail.Stdout = outFile
	muxtail.Stderr = os.Stderr
	// Put the process in its own group so we can kill the binary.
	muxtail.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logf("starting muxtail")
	if err := muxtail.Start(); err != nil {
		_ = outFile.Close()
		t.Fatal(err)
	}
	logf("muxtail pid %d started", muxtail.Process.Pid)
	// Register cleanup after t.TempDir() so it runs first (LIFO):
	// kill muxtail (releases its fd to output), close outFile, then TempDir removes the dir.
	t.Cleanup(func() {
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

	writeLines := func(path, prefix, char string) {
		<-start
		f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
		if err != nil {
			t.Errorf("open %s: %v", path, err)
			return
		}
		defer f.Close()
		payload := strings.Repeat(char, lineLength)
		w := bufio.NewWriterSize(f, 1<<20)
		for i := 0; i < numLines; i++ {
			fmt.Fprintf(w, "%s:%09d:%s\n", prefix, i, payload)
		}
		if err := w.Flush(); err != nil {
			t.Errorf("flush %s: %v", path, err)
		}
	}

	wg.Go(func() { writeLines(fileA, "AAA", "X") })
	wg.Go(func() { writeLines(fileB, "BBB", "Y") })
	close(start) // release both goroutines at the same instant

	wg.Wait()

	// Each output line: label(4) + prefix(3) + ":" + seq(9) + ":" + payload + "\n"
	outputLineSize := int64(4 + 3 + 1 + 9 + 1 + lineLength + 1)
	expectedLines := numLines * 2
	expectedSize := int64(expectedLines) * outputLineSize
	logf("writers done, output so far: %d / %d bytes", fileSize(output), expectedSize)

	// Poll until expected file size is reached or timeout.
	deadline := time.Now().Add(60 * time.Second)
	for {
		sz := fileSize(output)
		logf("poll: %d / %d bytes", sz, expectedSize)
		if sz >= expectedSize {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d / %d bytes", fileSize(output), expectedSize)
		}
		time.Sleep(time.Second)
	}
	logf("poll complete")

	syscall.Kill(-muxtail.Process.Pid, syscall.SIGKILL)
	muxtail.Wait()

	// Stream through the output file once for all checks — no full load into memory.
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

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
	if lineCount != expectedLines {
		t.Errorf("check 1 line count: got %d, want %d", lineCount, expectedLines)
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

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
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

// validLine checks that line matches: <label><prefix>:<9-digit-seq>:<payload>.
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
