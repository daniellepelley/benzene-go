package main

import (
	"testing"

	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push a native Kinesis stream
// record in the front door via benzenetest.SendKinesisStream, asserting on the batch-item failures
// the event source mapping reads back - the same harness shape every transport uses.

func TestOrderReceived_ValidRecordReportsNoFailure(t *testing.T) {
	failures := benzenetest.SendKinesisStream(t, benzenetest.NewHost(newApp()), "orders", "seq-1",
		order{ID: "o-1", Item: "widget", Amount: 9.99})
	if len(failures) != 0 {
		t.Errorf("failures = %v, want none for a valid order", failures)
	}
}

func TestOrderReceived_MissingIDReportsBatchFailure(t *testing.T) {
	// The handler rejects a record with no id: it is reported for redelivery by its SequenceNumber,
	// not silently checkpointed past.
	failures := benzenetest.SendKinesisStream(t, benzenetest.NewHost(newApp()), "orders", "seq-2",
		order{Item: "widget", Amount: 9.99})
	if len(failures) != 1 || failures[0] != "seq-2" {
		t.Errorf("failures = %v, want [seq-2]", failures)
	}
}

func TestUnroutedStreamReportsBatchFailure(t *testing.T) {
	// A record on a stream with no registered handler routes to not-found (unsuccessful) and is
	// reported for redelivery rather than dropped.
	failures := benzenetest.SendKinesisStream(t, benzenetest.NewHost(newApp()), "unknown-stream", "seq-3",
		order{ID: "o-3"})
	if len(failures) != 1 || failures[0] != "seq-3" {
		t.Errorf("failures = %v, want [seq-3] for an unrouted stream", failures)
	}
}
