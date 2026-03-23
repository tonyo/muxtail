package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Writer is a locked writer that ensures atomic line output.
type Writer struct {
	mu         sync.Mutex
	w          io.Writer
	timestamps bool
}

// WriteLine writes a labeled line atomically.
func (w *Writer) WriteLine(label, line string) {
	var ts string
	if w.timestamps {
		ts = time.Now().Format(time.RFC3339) + " "
	}
	w.mu.Lock()
	if w.timestamps {
		_, _ = fmt.Fprintf(w.w, "%s%s%s\n", ts, label, line)
	} else {
		_, _ = fmt.Fprintf(w.w, "%s%s\n", label, line)
	}
	w.mu.Unlock()
}
