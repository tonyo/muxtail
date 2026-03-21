# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build ./...          # build
go test ./...           # run all tests
go test -run TestName . # run a single test
go test -race ./...     # run tests with race detector
```

## Architecture

Single-package `main` CLI with three files:

- **main.go** — cobra root command, flag parsing (`-n`, `--label`/`-l`), label resolution (positional: user-supplied label or `filepath.Base(file) + " "`), goroutine fan-out with `sync.WaitGroup`, SIGINT/SIGTERM handling via context cancellation.
- **tailer.go** — two tailing strategies dispatched by `tailFile` / `tailStdin`:
  - Regular files: `emitLastN` (backward chunk scan via `lastNLines`) then `hpcloud/tail` in follow+reopen mode starting at EOF.
  - Stdin (`-`): `bufio.Scanner` in a goroutine, no initial-lines phase.
- **output.go** — `Writer` wraps `io.Writer` with a `sync.Mutex`; `WriteLine(label, line)` is the only write path, ensuring atomic output across all goroutines.

### Label convention
Labels are user-owned strings and are written verbatim before each line — users include their own spacing/brackets (e.g. `"[db] "`). Auto-generated labels (basename) get a trailing space appended automatically.

### `lastNLines` algorithm
Reads the file in 4096-byte chunks backwards, counting `\n` occurrences until `n+1` boundaries are found (or start-of-file). Files without a trailing newline have target decremented by 1 since EOF acts as a line terminator.
