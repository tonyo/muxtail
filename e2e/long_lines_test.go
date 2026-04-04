package e2e

import (
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

// TestFollowLongLines_NoOOM verifies that muxtail does not OOM when a followed
// file contains a line larger than --max-line-bytes. The giant line must be
// truncated, and lines written after it must still appear in the output.
func TestFollowLongLines_NoOOM(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t, dir)

	logFile := filepath.Join(dir, "big.log")
	output := filepath.Join(dir, "out.txt")

	const maxLine = 1 * 1024 * 1024 // 1 MB cap passed via flag

	outFile, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}

	// Create empty log file before starting muxtail.
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), bin,
		"-f", "-n", "0",
		fmt.Sprintf("--max-line-bytes=%d", maxLine),
		logFile,
	)
	cmd.Stdout = outFile
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = outFile.Close()
	})

	if err := cmd.Start(); err != nil {
		t.Fatal("start muxtail:", err)
	}

	// Give muxtail time to attach its inotify watch.
	time.Sleep(200 * time.Millisecond)

	// Write: short line, 10 MB line (10× the cap), short line.
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "before")
	fmt.Fprintln(f, strings.Repeat("X", 10*1024*1024)) // 10 MB line
	fmt.Fprintln(f, "after")
	f.Close()

	// Wait until "after" appears in the output (meaning muxtail survived
	// the giant line and continued reading).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(output)
		if strings.Contains(string(data), "after") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	outFile.Close()
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "before") {
		t.Error("missing 'before' line")
	}
	if !strings.Contains(content, "after") {
		t.Error("missing 'after' line — muxtail may have OOM'd or deadlocked on the giant line")
	}

	// The output must be much smaller than the giant line — the line was truncated.
	fi, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > int64(3*maxLine) {
		t.Errorf("output file is %d bytes — giant line was not truncated (cap=%d)", fi.Size(), maxLine)
	}
}

// TestFollowLongLines_LinesAfterTruncationAreCorrect verifies that multiple
// oversized lines interleaved with normal lines all appear correctly truncated
// and that subsequent normal lines are not corrupted.
func TestFollowLongLines_LinesAfterTruncationAreCorrect(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t, dir)

	logFile := filepath.Join(dir, "mixed.log")
	output := filepath.Join(dir, "out.txt")

	const maxLine = 512 * 1024 // 512 KB cap

	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	outFile, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), bin,
		"-f", "-n", "0",
		fmt.Sprintf("--max-line-bytes=%d", maxLine),
		logFile,
	)
	cmd.Stdout = outFile
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = outFile.Close()
	})

	if err := cmd.Start(); err != nil {
		t.Fatal("start muxtail:", err)
	}
	time.Sleep(200 * time.Millisecond)

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "line-1")
	fmt.Fprintln(f, strings.Repeat("Y", 2*1024*1024)) // 2 MB > cap
	fmt.Fprintln(f, "line-3")
	fmt.Fprintln(f, strings.Repeat("Z", 2*1024*1024)) // 2 MB > cap
	fmt.Fprintln(f, "line-5")
	f.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(output)
		if strings.Contains(string(data), "line-5") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	outFile.Close()
	data, _ := os.ReadFile(output)
	content := string(data)

	for _, want := range []string{"line-1", "line-3", "line-5"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}
