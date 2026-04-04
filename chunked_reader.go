package main

import "io"

// chunkedLineReader reads lines from r using a fixed-size buffer.
// It caps per-line memory at maxLine bytes: lines that exceed the cap are
// truncated (the first maxLine bytes are returned with truncated=true) and
// the remainder is discarded up to the next '\n'.
//
// On a partial read (data available but no '\n' yet), ReadLine retains the
// bytes read so far and returns ("", false, io.EOF) so the caller can wait
// for more data and retry. io.EOF from ReadLine is always transient — the
// caller should wait and try again when more data is available.
type chunkedLineReader struct {
	r        io.Reader
	buf      []byte // fixed read buffer, allocated once
	bufStart int    // start of unprocessed bytes in buf
	bufEnd   int    // end of unprocessed bytes in buf
	maxLine  int
	pending  []byte // bytes accumulated for the current line, not yet emitted
	overflow bool   // true while discarding bytes past maxLine until next '\n'
}

func newChunkedLineReader(r io.Reader, bufSize, maxLine int) *chunkedLineReader {
	return &chunkedLineReader{
		r:       r,
		buf:     make([]byte, bufSize),
		maxLine: maxLine,
	}
}

// ReadLine returns the next complete line (without trailing '\n').
// truncated is true when the line exceeded maxLine and was cut short.
// Returns ("", false, io.EOF) when no complete line is available yet;
// the caller should wait for more data and call ReadLine again.
// Any other non-nil error is fatal.
func (r *chunkedLineReader) ReadLine() (line string, truncated bool, err error) {
	for {
		// Process any bytes already in buf before reading more.
		if r.bufStart < r.bufEnd {
			chunk := r.buf[r.bufStart:r.bufEnd]

			// Find next newline.
			nl := -1
			for i, b := range chunk {
				if b == '\n' {
					nl = i
					break
				}
			}

			if nl == -1 {
				// No newline in this chunk — accumulate and consume it all.
				if !r.overflow {
					room := r.maxLine - len(r.pending)
					if room > 0 {
						take := len(chunk)
						if take > room {
							take = room
						}
						r.pending = append(r.pending, chunk[:take]...)
						if len(r.pending) >= r.maxLine {
							r.overflow = true
						}
					}
					// bytes beyond maxLine are dropped (overflow=true handles discard)
				}
				r.bufStart = r.bufEnd
			} else {
				// Newline found at chunk[nl].
				before := chunk[:nl]
				r.bufStart += nl + 1 // advance past '\n' for next call

				if r.overflow {
					// Discarding mode ends at this newline — emit truncated line.
					r.overflow = false
					result := string(r.pending)
					r.pending = r.pending[:0]
					return result, true, nil
				}

				// Accumulate bytes before the newline.
				room := r.maxLine - len(r.pending)
				truncLine := false
				if len(before) > room {
					before = before[:room]
					truncLine = true
				}
				r.pending = append(r.pending, before...)

				result := string(r.pending)
				r.pending = r.pending[:0]
				return result, truncLine, nil
			}
			continue
		}

		// buf exhausted — read more data.
		// io.EOF is transient for tailed files: always try reading again.
		n, readErr := r.r.Read(r.buf)
		r.bufStart = 0
		r.bufEnd = n
		if n == 0 {
			// No data this time; return EOF so the caller can wait.
			if readErr == nil {
				// Unusual: zero bytes, no error — treat as EOF to avoid spin.
				return "", false, io.EOF
			}
			return "", false, readErr
		}
		// We have n > 0 bytes; ignore readErr for now and process them.
		// If readErr != nil (including io.EOF), the next Read will repeat it.
	}
}
