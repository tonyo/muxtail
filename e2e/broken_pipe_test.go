package e2e

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestBrokenPipe verifies that muxtail exits promptly when its stdout pipe
// is closed (e.g. muxtail -f file | head -N). Before the EPIPE fix,
// muxtail would spin indefinitely following the file.
func TestBrokenPipe(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t, dir)

	logFile := filepath.Join(dir, "active.log")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Write lines continuously to the log file so muxtail always has data
	// to write and will hit EPIPE quickly once the pipe reader closes.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				fmt.Fprintf(w, "line %d\n", i)
				w.Flush()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// Give the writer a head start.
	time.Sleep(50 * time.Millisecond)

	// muxtail | head -3: head exits after 3 lines, closing the read end of
	// the pipe. muxtail must detect the broken pipe and exit cleanly.
	muxtailCmd := exec.CommandContext(context.Background(), bin, "-f", "-n", "0", logFile)
	headCmd := exec.CommandContext(ctx, "head", "-3")

	pipe, err := muxtailCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	headCmd.Stdin = pipe
	headCmd.Stdout = os.Stdout

	if err := muxtailCmd.Start(); err != nil {
		t.Fatal("start muxtail:", err)
	}
	if err := headCmd.Start(); err != nil {
		t.Fatal("start head:", err)
	}
	// Close our copy of the read end so head holds the only reference.
	// When head exits, the pipe is fully closed and muxtail gets EPIPE.
	pipe.Close()

	// Wait for head to finish (it exits as soon as it reads 3 lines).
	headDone := make(chan error, 1)
	go func() { headDone <- headCmd.Wait() }()

	select {
	case err := <-headDone:
		if err != nil {
			t.Logf("head exited with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("head did not finish reading 3 lines within 5s")
	}

	// muxtail should exit shortly after head closes the pipe.
	muxtailDone := make(chan error, 1)
	go func() { muxtailDone <- muxtailCmd.Wait() }()

	select {
	case err := <-muxtailDone:
		// A write error (broken pipe) is expected — the important thing is
		// that it exits rather than hanging.
		t.Logf("muxtail exited with: %v", err)
	case <-time.After(3 * time.Second):
		muxtailCmd.Process.Kill()
		t.Fatal("muxtail did not exit after pipe was closed (broken pipe not detected)")
	}
}
