package main

import (
	"testing"

	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push a native MSK/Kafka
// record in the front door via benzenetest.SendKafkaEvent, asserting on the batch-item failures the
// event source mapping reads back - the same harness shape every transport uses. A Kafka failure is
// reported "{partition}@{offset}" (the topic-partition group key and the resume offset).

func TestOrderReceived_ValidRecordReportsNoFailure(t *testing.T) {
	failures := benzenetest.SendKafkaEvent(t, benzenetest.NewHost(newApp()), "orders", 0, 41,
		order{ID: "o-1", Item: "widget", Amount: 9.99})
	if len(failures) != 0 {
		t.Errorf("failures = %v, want none for a valid order", failures)
	}
}

func TestOrderReceived_MissingIDReportsBatchFailure(t *testing.T) {
	// The handler rejects a record with no id: it is reported for redelivery by its {partition,
	// offset}, not silently committed past.
	failures := benzenetest.SendKafkaEvent(t, benzenetest.NewHost(newApp()), "orders", 0, 42,
		order{Item: "widget", Amount: 9.99})
	if len(failures) != 1 || failures[0] != "orders-0@42" {
		t.Errorf("failures = %v, want [orders-0@42]", failures)
	}
}

func TestUnroutedTopicReportsBatchFailure(t *testing.T) {
	// A record on a topic with no registered handler routes to not-found (unsuccessful) and is
	// reported for redelivery rather than dropped.
	failures := benzenetest.SendKafkaEvent(t, benzenetest.NewHost(newApp()), "unknown-topic", 0, 7,
		order{ID: "o-3"})
	if len(failures) != 1 || failures[0] != "unknown-topic-0@7" {
		t.Errorf("failures = %v, want [unknown-topic-0@7] for an unrouted topic", failures)
	}
}
