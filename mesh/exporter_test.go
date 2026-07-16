package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewLogExporter_NilWriterMeansStdout(t *testing.T) {
	exporter := NewLogExporter(nil)

	if exporter.w != os.Stdout {
		t.Errorf("w = %v, want os.Stdout", exporter.w)
	}
}

func TestLogExporter_WritesOneJSONLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	exporter := NewLogExporter(&buf)

	exporter.Export(context.Background(), TraceEvent{
		TraceID:   "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:    "00f067aa0ba902b7",
		Topic:     "order:create",
		Status:    "Ok",
		StartedAt: time.Date(2026, 7, 16, 9, 14, 3, 0, time.UTC),
	})

	line := buf.String()
	if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		t.Fatalf("output = %q, want exactly one newline-terminated line", line)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("line is not valid JSON: %v: %q", err, line)
	}
	if raw["topic"] != "order:create" || raw["status"] != "Ok" {
		t.Errorf("line = %q, want topic/status present", line)
	}
}

func TestLogExporter_ConcurrentExportsDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	exporter := NewLogExporter(&buf)
	const n = 20

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			exporter.Export(context.Background(), TraceEvent{Topic: "order:create", Status: "Ok"})
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("wrote %d lines, want %d", len(lines), n)
	}
	for i, line := range lines {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("line %d is not valid JSON: %v: %q", i, err, line)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sink is down") }

func TestLogExporter_FailingSinkIsDroppedSilently(t *testing.T) {
	exporter := NewLogExporter(failingWriter{})

	// Must neither panic nor surface the error anywhere - the feed is lossy by design.
	exporter.Export(context.Background(), TraceEvent{Topic: "order:create", Status: "Ok"})
}
