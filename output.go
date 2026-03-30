package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Writer is a locked writer that ensures atomic line output.
type Writer struct {
	mu         sync.Mutex
	w          io.Writer
	e          io.Writer // stderr destination; if nil, os.Stderr is used
	timestamps bool
	nowFn      func() time.Time // if nil, uses time.Now
}

// WriteLine writes a labeled line atomically.
func (w *Writer) WriteLine(label, line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timestamps {
		now := w.nowFn
		if now == nil {
			now = time.Now
		}
		fmt.Fprintf(w.w, "%s %s%s\n", now().Format(time.RFC3339), label, line)
	} else {
		fmt.Fprintf(w.w, "%s%s\n", label, line)
	}
}

// WriteError writes a diagnostic message to the error writer atomically.
func (w *Writer) WriteError(msg string) {
	e := w.e
	if e == nil {
		e = os.Stderr
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprint(e, msg)
}
