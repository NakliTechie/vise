package cli

import (
	"fmt"
	"io"
	"sync"
)

// resultDelivery serializes the CLI's two streams, including raw probe mirror
// goroutines. After a failed write no further result bytes are attempted.
// The underlying writer remains caller-owned: we neither close nor flush it.
type resultDelivery struct {
	mu  sync.Mutex
	err error
}

func (d *resultDelivery) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

type resultStream struct {
	delivery *resultDelivery
	writer   io.Writer
	name     string
}

func (w *resultStream) Write(p []byte) (int, error) {
	d := w.delivery
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return 0, d.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		d.err = fmt.Errorf("%s: %w", w.name, err)
	}
	return n, err
}
