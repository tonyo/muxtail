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
	w.mu.Lock()
	if w.timestamps {
		fmt.Fprintf(w.w, "%s %s%s\n", time.Now().Format("2006-01-02T15:04:05"), label, line)
	} else {
		fmt.Fprintf(w.w, "%s%s\n", label, line)
	}
	w.mu.Unlock()
}
