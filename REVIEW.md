# Code Review: muxtail

## Summary

Thorough review of a well-structured ~400-line Go CLI that multiplexes `tail -f` across files. The codebase is clean, well-tested, and thoughtfully designed. Tests pass, lint is clean. The issues below are ordered by severity.

---

## Issues Found

### 1. [Bug] Only the first error is surfaced when multiple files fail

**File:** `main.go:169-173`

When tailing multiple files, `run()` iterates `errCh` and returns on the *first* non-nil error, silently discarding all others. If the user runs `muxtail /missing1.log /missing2.log`, they only learn about the first failure.

**Fix:** Collect all errors and return a joined error (or print each to stderr before returning a summary error).

---

### 2. [Bug] Negative `--lines` value silently accepted

**File:** `main.go:66`, `tailer.go:111`

`cobra` happily accepts `--lines=-5`. The `n <= 0` guard in `emitLastN` makes it behave as "show no lines", but the user gets no feedback that their input was invalid.

**Fix:** Validate `flagLines >= 0` at the top of `run()` and return an error.

---

### 3. [Correctness] Timestamp captured outside the mutex

**File:** `output.go:19-23`

The timestamp is sampled *before* `w.mu.Lock()`. Under contention, a goroutine that sampled its timestamp first could write *after* another goroutine, producing out-of-order timestamps. Moving the sampling inside the lock ensures timestamps match write order.

**Fix:** Move `time.Now()` call to after `w.mu.Lock()`.

---

### 4. [Performance] Unbounded memory allocation in `lastNLines`

**File:** `tailer.go:190`

`data := make([]byte, size-startAbs)` allocates the entire tail segment in one shot. With a pathologically large `--lines` value on a multi-GB log file, this could exhaust memory. The backward scan (efficient) is good, but the forward read should stream.

**Fix:** Stream the forward pass through `bufio.Scanner` directly from the seeked file instead of reading into a single `[]byte`. (Or cap `--lines` to a sane maximum, e.g., 100,000.)

---

### 5. [Performance] `bufio.Scanner` default buffer silently truncates long lines

**File:** `tailer.go:195-200`, `tailer.go:205`

The default `bufio.Scanner` buffer is 64 KB. Lines longer than this are silently truncated with no error, which is especially problematic for JSON log lines.

**Fix:** Call `scanner.Buffer(buf, maxSize)` with a larger limit (e.g., 1 MB), or document the limitation.

---

### 6. [Best Practice] `NO_COLOR` environment variable not respected

**File:** `main.go:129`

The [NO_COLOR](https://no-color.org/) convention is a widely adopted de facto standard. Many CLI tools check `os.Getenv("NO_COLOR") != ""` to disable colors.

**Fix:** Add `os.Getenv("NO_COLOR") != ""` as an additional condition alongside `--no-color`.

---

### 7. [Robustness] Write errors silently discarded in `Writer.WriteLine`

**File:** `output.go:25-28`

`fmt.Fprintf` return values are ignored. If stdout is a broken pipe (e.g., `muxtail -f app.log | head -5`), the tool keeps running silently, burning CPU following the file.

**Fix:** Return or track the error. On write failure (especially `EPIPE`), propagate cancellation to stop tailing.

---

### 8. [Robustness] Unsynchronized stderr writes from multiple goroutines

**File:** `tailer.go:34`, `tailer.go:89`

Multiple tailer goroutines write to `os.Stderr` via `fmt.Fprintf` without synchronization. While kernel-level atomicity usually prevents garbled output for short writes, it's not guaranteed by Go's runtime.

**Fix:** Route stderr diagnostics through the same `Writer` (or a separate synchronized writer) for consistency.

---

### 9. [Code Quality] Unnecessary loop variable capture

**File:** `main.go:155`

`spec := spec` was required pre-Go 1.22 to avoid closure capture bugs. As of Go 1.22+ (this project uses Go 1.26), the loop variable is per-iteration. The re-binding is dead code.

**Fix:** Remove `spec := spec`.

---

### 10. [Code Quality] Mutex unlock not deferred

**File:** `output.go:23-29`

`w.mu.Unlock()` is called explicitly instead of via `defer`. The current code is simple enough that this is safe, but it's a maintenance hazard — any future code added between lock/unlock that could panic would deadlock.

**Fix:** Use `defer w.mu.Unlock()`.

---

### 11. [Code Quality] Hardcoded initial slice capacity

**File:** `tailer.go:196`

`lines := make([]string, 0, 128)` — the capacity is arbitrary. Since `n` is known, `min(n, 128)` would be more precise and avoid over-allocating for small `n`.

**Fix:** Use `min(n, someReasonableCap)`.

---

### 12. [Documentation] `tailStdin` goroutine can leak when stdin blocks

**File:** `tailer.go:209-218`

If `scanner.Scan()` is blocked on a pipe that never closes, cancelling the context returns from `tailStdin` and closes `done`, but the goroutine remains stuck inside `Scan()` until the reader is closed externally. This is a known Go limitation with blocking I/O and no clean solution exists without closing the reader, but it should be documented with a comment.

**Fix:** Add a comment explaining the leak and why it's acceptable.

---

## Proposed Fix Order

Fixes are independent and can be done in parallel:

1. **#1** — Multiple error reporting (bug)
2. **#2** — Negative lines validation (bug)
3. **#3** — Timestamp inside lock (correctness)
4. **#6** — NO_COLOR support (best practice)
5. **#7** — Handle write errors / EPIPE (robustness)
6. **#4** — Stream forward pass in lastNLines (performance)
7. **#5** — Increase scanner buffer (performance)
8. **#8** — Synchronized stderr (robustness)
9. **#9, #10, #11** — Quick cleanups (code quality)
10. **#12** — Document goroutine leak (documentation)
