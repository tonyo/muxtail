package main

import (
	"fmt"
	"io"
	"sync"
)

// Writer is a locked writer that ensures atomic line output.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// WriteLine writes a labeled line atomically.
func (w *Writer) WriteLine(label, line string) {
	w.mu.Lock()
	fmt.Fprintf(w.w, "%s%s\n", label, line)
	w.mu.Unlock()
}
