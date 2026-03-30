package e2e

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLogRotation verifies that muxtail -F (follow-retry) handles log rotation
// correctly: no lines are lost or duplicated across the rename+recreate boundary.
//
// Sequence:
//  1. Write N "pre-rotation" lines to app.log
//  2. muxtail -F starts following app.log
//  3. Rename app.log → app.log.1  (simulates logrotate)
//  4. Create new app.log, write N "post-rotation" lines
//  5. Verify all pre and post lines appear exactly once in output
func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t, dir)

	logFile := filepath.Join(dir, "app.log")
	rotated := filepath.Join(dir, "app.log.1")
	output := filepath.Join(dir, "output.txt")

	const preN = 20
	const postN = 20

	// Write pre-rotation lines before muxtail starts.
	pre, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < preN; i++ {
		fmt.Fprintf(pre, "pre-%04d\n", i)
	}
	pre.Close()

	outFile, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}

	// Start muxtail with -F (retry) so it survives the file disappearing briefly.
	muxtailCmd := exec.CommandContext(context.Background(), bin, "-F", "-n", fmt.Sprintf("%d", preN), logFile)
	muxtailCmd.Stdout = outFile
	muxtailCmd.Stderr = os.Stderr
	muxtailCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := muxtailCmd.Start(); err != nil {
		_ = outFile.Close()
		t.Fatal("start muxtail:", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-muxtailCmd.Process.Pid, syscall.SIGKILL)
		_ = muxtailCmd.Wait()
		_ = outFile.Close()
	})

	// Give muxtail time to emit the initial lines and attach the inotify watch.
	time.Sleep(300 * time.Millisecond)

	// Rotate: rename the current log, create a new one with post-rotation lines.
	if err := os.Rename(logFile, rotated); err != nil {
		t.Fatal("rename:", err)
	}
	newLog, err := os.Create(logFile)
	if err != nil {
		t.Fatal("create new log:", err)
	}
	w := bufio.NewWriter(newLog)
	for i := 0; i < postN; i++ {
		fmt.Fprintf(w, "post-%04d\n", i)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	newLog.Close()

	// Wait until the expected number of lines appear in output.
	expectedLines := preN + postN
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := countLines(output)
		if got >= expectedLines {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d / %d lines in output", got, expectedLines)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Stop muxtail cleanly.
	syscall.Kill(-muxtailCmd.Process.Pid, syscall.SIGTERM)
	muxtailCmd.Wait()
	outFile.Close()

	// Verify output: all pre and post lines present exactly once, no duplicates.
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	seen := make(map[string]int)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			seen[line]++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < preN; i++ {
		key := fmt.Sprintf("pre-%04d", i)
		if seen[key] != 1 {
			t.Errorf("pre-rotation line %q: count=%d, want 1", key, seen[key])
		}
	}
	for i := 0; i < postN; i++ {
		key := fmt.Sprintf("post-%04d", i)
		if seen[key] != 1 {
			t.Errorf("post-rotation line %q: count=%d, want 1", key, seen[key])
		}
	}
}

// countLines returns the number of newline-terminated lines in path.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	return n
}
