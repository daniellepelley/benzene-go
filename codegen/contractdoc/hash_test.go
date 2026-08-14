package contractdoc

import (
	"strings"
	"testing"
)

const reservedDoc = `{
  "openapi": "3.0.1",
  "info": {"title": "x", "version": "1"},
  "messageEndpoint": "/benzene/invoke",
  "transports": ["http"],
  "requests": [
    {"topic": "benzene:spec", "reserved": true, "request": {}, "response": {}, "example": {"a": 1}}
  ],
  "events": [
    {"topic": "e", "message": {}, "example": {"b": 2}}
  ],
  "components": {"schemas": {}}
}`

func TestNormalize_StripsDecorationAndReservedFlag(t *testing.T) {
	doc := mustParse(t, reservedDoc)
	normalized := Normalize(doc, true)

	if _, ok := normalized.Data["messageEndpoint"]; ok {
		t.Error("messageEndpoint should be stripped")
	}
	if _, ok := normalized.Data["transports"]; ok {
		t.Error("transports should be stripped")
	}

	requests := normalized.Requests()
	if len(requests) != 1 {
		t.Fatalf("topicScoped=true should keep the reserved entry, got %d requests", len(requests))
	}
	if _, hasFlag := requests[0]["reserved"]; hasFlag {
		t.Error("the reserved flag itself should always be stripped")
	}
	if _, hasExample := requests[0]["example"]; hasExample {
		t.Error("example should be stripped from a surviving request")
	}

	events := normalized.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if _, hasExample := events[0]["example"]; hasExample {
		t.Error("example should be stripped from events too")
	}
}

func TestNormalize_WholeServiceDropsReservedEntryEntirely(t *testing.T) {
	doc := mustParse(t, reservedDoc)
	normalized := Normalize(doc, false)

	if len(normalized.Requests()) != 0 {
		t.Errorf("topicScoped=false should drop the reserved entry entirely, got %v", normalized.Requests())
	}
}

func TestNormalize_DoesNotMutateSource(t *testing.T) {
	doc := mustParse(t, reservedDoc)
	_ = Normalize(doc, false)

	if _, ok := doc.Data["messageEndpoint"]; !ok {
		t.Error("Normalize must not mutate the source document")
	}
	if len(doc.Requests()) != 1 {
		t.Error("Normalize must not mutate the source document's requests")
	}
	if _, hasFlag := doc.Requests()[0]["reserved"]; !hasFlag {
		t.Error("Normalize must not strip the source document's own reserved flag")
	}
}

func TestHash_FormatAndDeterminism(t *testing.T) {
	doc := mustParse(t, minimalDoc)

	h1, err := Hash(doc, false)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	h2, err := Hash(doc, false)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("Hash is not deterministic: %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("Hash() = %q, want sha256: prefix", h1)
	}
	if len(h1) != len("sha256:")+64 {
		t.Errorf("Hash() length = %d, want %d", len(h1), len("sha256:")+64)
	}
}

func TestHash_ReservedEntryChangesHashOnlyWhenTopicScoped(t *testing.T) {
	doc := mustParse(t, reservedDoc)

	wholeService, err := Hash(doc, false)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	topicScoped, err := Hash(doc, true)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if wholeService == topicScoped {
		t.Error("a surviving reserved entry should change the hash under topicScoped=true")
	}
}
