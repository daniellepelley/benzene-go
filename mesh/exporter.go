package mesh

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
)

// LogExporter writes one JSON line per TraceEvent to a writer - the zero-setup exporter:
// structured, greppable invocation logs on stdout, which every platform this module
// targets (Lambda, Functions, Cloud Run, plain processes) already collects. The push
// exporter that feeds a meshd collector is a later phase (mesh.md §8).
type LogExporter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewLogExporter returns a LogExporter writing to w; a nil w means os.Stdout.
func NewLogExporter(w io.Writer) *LogExporter {
	if w == nil {
		w = os.Stdout
	}
	return &LogExporter{w: w}
}

// Export writes event as a single JSON line. Writes are serialized so concurrent
// invocations never interleave bytes within a line. A write failure is deliberately
// dropped: the feed is lossy by design, because an ailing log sink must never make the
// service slower or less reliable than an unmeshed one.
func (e *LogExporter) Export(_ context.Context, event TraceEvent) {
	// TraceEvent contains only strings, floats and a time.Time, so Marshal cannot fail.
	data, _ := json.Marshal(event)
	data = append(data, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(data)
}
