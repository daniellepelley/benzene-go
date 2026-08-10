package main

import (
	"testing"

	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push a native S3 event
// notification in the front door via benzenetest.SendS3Event, asserting on the Go error the binding
// returns to the Lambda runtime (nil on success; non-nil triggers AWS's async-invoke retry).

func TestObjectUploaded_ValidObjectSucceeds(t *testing.T) {
	if err := benzenetest.SendS3Event(t, benzenetest.NewHost(newApp()), "uploads", "ObjectCreated:Put", "photo.jpg"); err != nil {
		t.Errorf("SendS3Event() error = %v, want nil for a valid object", err)
	}
}

func TestUnhandledBucketOrEventReturnsError(t *testing.T) {
	// Only "uploads:ObjectCreated:Put" is registered; a different bucket routes to not-found
	// (unsuccessful), which surfaces as a Go error for async-invoke retry rather than a silent drop.
	if err := benzenetest.SendS3Event(t, benzenetest.NewHost(newApp()), "other-bucket", "ObjectCreated:Put", "photo.jpg"); err == nil {
		t.Error("SendS3Event() for an unregistered bucket = nil error, want a Go error")
	}
}
