package integration

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

	muxtail := exec.Command("go", "run", "..", "-f", "-n", "0",
		"--label", "[A] ", "--label", "[B] ",
		fileA, fileB)
	muxtail.Stdout = outFile
	muxtail.Stderr = os.Stderr
	if err := muxtail.Start(); err != nil {
		outFile.Close()
		t.Fatal(err)
	}
	// Register cleanup after t.TempDir() so it runs first (LIFO):
	// kill muxtail (releases its fd to output), close outFile, then TempDir removes the dir.
	t.Cleanup(func() {
		muxtail.Process.Kill()
		muxtail.Wait()
		outFile.Close()
	})

	// Give muxtail time to attach inotify watches.
	time.Sleep(200 * time.Millisecond)

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

	go writeLines(fileA, "AAA", "X")
	go writeLines(fileB, "BBB", "Y")
	close(start) // release both goroutines at the same instant

	wg.Wait()

	// Poll until expected line count appears or timeout.
	expected := numLines * 2
	deadline := time.Now().Add(60 * time.Second)
	for {
		data, _ := os.ReadFile(output)
		if bytes.Count(data, []byte{'\n'}) >= expected {
			break
		}
		if time.Now().After(deadline) {
			data, _ = os.ReadFile(output)
			t.Fatalf("timeout: got %d / %d lines", bytes.Count(data, []byte{'\n'}), expected)
		}
		time.Sleep(time.Second)
	}

	muxtail.Process.Kill()
	muxtail.Wait()

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	// Check 1: line count.
	if len(lines) != expected {
		t.Errorf("check 1 line count: got %d, want %d", len(lines), expected)
	}

	// Check 2: pattern integrity — label prefix + 9-digit seq + payload of correct char/length.
	payloadX := strings.Repeat("X", lineLength)
	payloadY := strings.Repeat("Y", lineLength)
	invalid := 0
	for _, line := range lines {
		if !validLine(line, "[A] ", "AAA", payloadX) &&
			!validLine(line, "[B] ", "BBB", payloadY) {
			invalid++
			if invalid <= 5 {
				end := min(len(line), 80)
				t.Logf("invalid line: %q", line[:end])
			}
		}
	}
	if invalid > 0 {
		t.Errorf("check 2 pattern integrity: %d invalid lines", invalid)
	}

	// Check 3: cross-contamination.
	aWithBBB, bWithAAA := 0, 0
	for _, line := range lines {
		if strings.HasPrefix(line, "[A]") && strings.Contains(line, "BBB") {
			aWithBBB++
		}
		if strings.HasPrefix(line, "[B]") && strings.Contains(line, "AAA") {
			bWithAAA++
		}
	}
	if aWithBBB != 0 || bWithAAA != 0 {
		t.Errorf("check 3 cross-contamination: [A]+BBB=%d [B]+AAA=%d", aWithBBB, bWithAAA)
	}

	// Check 4: sequence completeness.
	seqRe := regexp.MustCompile(`([AB]{3}):(\d{9}):`)
	seqA := make(map[string]int, numLines)
	seqB := make(map[string]int, numLines)
	for _, line := range lines {
		m := seqRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "AAA":
			seqA[m[2]]++
		case "BBB":
			seqB[m[2]]++
		}
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
