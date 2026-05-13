package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

type streamFlushWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	flusher   http.Flusher
	policy    string
	interval  time.Duration
	lastFlush time.Time
	buffer    bytes.Buffer
}

func newStreamFlushWriter(writer io.Writer, flusher http.Flusher) *streamFlushWriter {
	settings := CurrentRuntimeSettings()
	return &streamFlushWriter{
		writer:   writer,
		flusher:  flusher,
		policy:   settings.StreamFlushPolicy,
		interval: currentStreamFlushInterval(),
	}
}

func (w *streamFlushWriter) WriteString(data string) error {
	if w == nil || w.writer == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.policy != StreamFlushPolicyCoalesce {
		if _, err := io.WriteString(w.writer, data); err != nil {
			return err
		}
		w.flushTransport()
		return nil
	}
	if _, err := w.buffer.WriteString(data); err != nil {
		return err
	}
	if w.lastFlush.IsZero() || time.Since(w.lastFlush) >= w.interval {
		return w.flushLocked()
	}
	return nil
}

func (w *streamFlushWriter) WriteBytes(data []byte) error {
	if w == nil || len(data) == 0 {
		return nil
	}
	return w.WriteString(string(data))
}

func (w *streamFlushWriter) Flush() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *streamFlushWriter) flushLocked() error {
	if w.buffer.Len() > 0 {
		if _, err := w.writer.Write(w.buffer.Bytes()); err != nil {
			return err
		}
		w.buffer.Reset()
	}
	w.flushTransport()
	return nil
}

func (w *streamFlushWriter) flushTransport() {
	if w == nil || w.flusher == nil {
		return
	}
	w.flusher.Flush()
	w.lastFlush = time.Now()
}

func startStreamKeepalive(ctx context.Context, w *streamFlushWriter) func() error {
	if w == nil {
		return func() error { return nil }
	}
	interval := currentStreamKeepaliveInterval()
	if interval <= 0 {
		return func() error { return nil }
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		defer close(stopped)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if err := w.WriteString(": codex2api-keepalive\n\n"); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}
	}()

	var stopOnce sync.Once
	return func() error {
		stopOnce.Do(func() {
			close(done)
			<-stopped
		})
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
}
