# Future Work

## Unbounded line length support

### Background

muxtail currently caps line length at 1 MB (`scannerMaxBuf` in `tailer.go`).
Lines longer than this are rejected with `bufio.ErrTooLong`.

GNU `tail` has no such limit because it never buffers a complete line — it
copies raw bytes in fixed-size chunks from the seek offset to stdout.
muxtail uses `bufio.Scanner`, which must assemble the full token in a
contiguous buffer before returning it as a `string`.

### What needs to change

#### 1. `Writer.WriteLine` → `Writer.WriteChunked`

The current signature is:

```go
func (w *Writer) WriteLine(label, line string) 
```

It writes `label + line + "\n"` atomically under the mutex. To stream a
line in chunks, the mutex must be held across all chunks of a single line,
but data should flow through without accumulating in memory.

Proposed new path:

```go
func (w *Writer) WriteLineFrom(label string, r io.Reader) error
```

Acquires the mutex, writes the label, then copies from `r` to `w.w` in a
fixed-size chunk loop (e.g. `io.Copy` with a stack-allocated 32 KB buf),
appends `"\n"`, then releases. This keeps atomicity and constant memory.

#### 2. `lastNLines` forward pass

Replace `bufio.NewScanner(r)` with `bufio.NewReader(r)` and
`ReadString('\n')` (which grows dynamically) — or better, stream directly:

```go
// seek r to startAbs, then for each line:
//   prefix := label
//   copy chunks from r to w until '\n'
```

This eliminates the per-line string allocation entirely for the forward
pass.

#### 3. `tailStdin`

Replace the `scanner.Scan()` goroutine with a `bufio.Reader`-based loop
that pipes directly into the writer without buffering full lines.

#### 4. `nxadm/tail` follow phase

`nxadm/tail` uses `bufio.Reader.ReadString('\n')` internally, which grows
without bound — already safe for long lines, no change needed.

### Complexity

Medium. The main challenge is restructuring `Writer` to accept a streaming
source while maintaining atomicity. Timestamp prepending also needs to
happen inside the lock before the first chunk is written.

### Tests to add

- `TestWriter_WriteLineFrom_LongLine`: stream a >1 MB line, verify output
- `TestLastNLines_UnboundedLine`: verify no cap on line length in backward
  scan path
- `TestTailStdin_UnboundedLine`: verify stdin path handles >1 MB lines
