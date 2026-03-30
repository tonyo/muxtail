package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles the muxtail binary into dir and returns its path.
func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "muxtail")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "..")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal("build failed:", err)
	}
	return bin
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
