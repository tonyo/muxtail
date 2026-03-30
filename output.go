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

// WriteLine writes a labeled line atomically. Returns any write error.
func (w *Writer) WriteLine(label, line string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if w.timestamps {
		now := w.nowFn
		if now == nil {
			now = time.Now
		}
		_, err = fmt.Fprintf(w.w, "%s %s%s\n", now().Format(time.RFC3339), label, line)
	} else {
		_, err = fmt.Fprintf(w.w, "%s%s\n", label, line)
	}
	return err
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
