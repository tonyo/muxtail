# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build ./...          # build
go test ./...           # run all tests
go test -run TestName . # run a single test
go test -race ./...     # run tests with race detector
make vet                # run go vet
make lint               # run golangci-lint
make fmt                # format files
```

Run `make fmt && make vet && make lint` after every change to Go files.

## Architecture

Single-package `main` CLI with three files:

- **main.go** — cobra root command, flag parsing, label resolution, color assignment, goroutine fan-out with `sync.WaitGroup`, SIGINT/SIGTERM handling via context cancellation.
- **tailer.go** — two tailing strategies dispatched by `tailFile` / `tailStdin`:
  - Regular files: `emitLastN` (backward chunk scan via `lastNLines`) then `hpcloud/tail` in follow+reopen mode starting at EOF.
  - Stdin (`-`): `bufio.Scanner` in a goroutine, no initial-lines phase.
- **output.go** — `Writer` wraps `io.Writer` with a `sync.Mutex`; `WriteLine(label, line)` is the only write path, ensuring atomic output across all goroutines.

### Label convention
Labels are user-owned strings and are written verbatim before each line — users include their own spacing/brackets (e.g. `"[db] "`). Auto-generated labels (basename) get a trailing space appended automatically. ANSI color codes are pre-applied to labels in `main.go` (before passing to tailers) when stdout is a terminal and `--no-color` is not set.

### Color assignment
`colorizeLabel(label, code)` wraps a non-empty label with an ANSI escape + reset. Colors cycle through `ansiColors` (10 entries) indexed by file position. Terminal detection uses `os.File.Stat()` + `os.ModeCharDevice`. Empty labels (no prefix mode, no `--label`) are left uncolored.

### `lastNLines` algorithm
Reads the file in 4096-byte chunks backwards, counting `\n` occurrences until `n+1` boundaries are found (or start-of-file). Files without a trailing newline have target decremented by 1 since EOF acts as a line terminator.
