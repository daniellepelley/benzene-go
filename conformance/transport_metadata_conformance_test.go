package conformance

import (
	"testing"

	"github.com/daniellepelley/benzene-go/wire"
)

// transport-metadata-cases.json pins the native-metadata topic resolution of wire-contracts.md
// §2 - the reserved `topic` key (tier A, configurable), the case-insensitive read, the
// remaining-metadata-becomes-headers rule, and the "only the configured key routes" guard - plus
// that an overridden key is honoured. In this port that resolution lives in the shared
// wire.ResolveMetadataTopic primitive, which every native-metadata inbound binding (awssqs,
// awssns, gcppubsub) delegates to, so exercising the primitive exercises the real resolution
// path. The version-carrying case (tier C, payload versioning) now runs too: the version
// travels as a header alongside the topic (the fixture's headersExclude lists only the topic
// key, not the version key), read by wire.ResolveVersion the same way RouterMiddleware reads it
// off ic.Headers downstream. See conformance/README.md and ROADMAP.md.

type transportMetadataFixture struct {
	DefaultMetadataKeys struct {
		Topic   string `json:"topic"`
		Version string `json:"version"`
	} `json:"defaultMetadataKeys"`
	MetadataCases []metadataCase `json:"metadataCases"`
	OverrideCases []struct {
		Name         string                 `json:"name"`
		MetadataKeys struct{ Topic string } `json:"metadataKeys"`
		Metadata     map[string]string      `json:"metadata"`
		Expected     metadataExpectation    `json:"expected"`
	} `json:"overrideCases"`
}

type metadataCase struct {
	Name     string              `json:"name"`
	Requires string              `json:"requires"`
	Metadata map[string]string   `json:"metadata"`
	Expected metadataExpectation `json:"expected"`
}

type metadataExpectation struct {
	Topic          string            `json:"topic"`
	Version        string            `json:"version"`
	Headers        map[string]string `json:"headers"`
	HeadersExclude []string          `json:"headersExclude"`
}

func TestConformance_TransportMetadata(t *testing.T) {
	var fixture transportMetadataFixture
	loadFixture(t, "transport-metadata-cases.json", &fixture)
	requireCases(t, len(fixture.MetadataCases), "transport-metadata-cases", "metadataCases")
	requireCases(t, len(fixture.OverrideCases), "transport-metadata-cases", "overrideCases")

	defaultKey := fixture.DefaultMetadataKeys.Topic
	versionKey := fixture.DefaultMetadataKeys.Version

	for _, tc := range fixture.MetadataCases {
		t.Run(tc.Name, func(t *testing.T) {
			assertResolution(t, tc.Metadata, defaultKey, versionKey, tc.Expected)
		})
	}

	for _, oc := range fixture.OverrideCases {
		t.Run(oc.Name, func(t *testing.T) {
			assertResolution(t, oc.Metadata, oc.MetadataKeys.Topic, versionKey, oc.Expected)
		})
	}
}

func assertResolution(t *testing.T, metadata map[string]string, topicKey, versionKey string, expected metadataExpectation) {
	t.Helper()
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)

	if topic != expected.Topic {
		t.Errorf("topic = %q, want %q", topic, expected.Topic)
	}
	// The version travels as a header (tier C), not under the topic key: ResolveMetadataTopic
	// strips only the topic key, and ResolveVersion reads the version out of the remaining
	// headers exactly as RouterMiddleware does off ic.Headers. expected.version is "" for every
	// non-versioning case, which ResolveVersion also yields when no version header is present.
	if version := wire.ResolveVersion(headers, []string{versionKey}); version != expected.Version {
		t.Errorf("version = %q, want %q", version, expected.Version)
	}
	for name, want := range expected.Headers {
		if got, ok := headers[name]; !ok || got != want {
			t.Errorf("headers[%q] = %q (present=%v), want %q", name, got, ok, want)
		}
	}
	for _, name := range expected.HeadersExclude {
		if _, ok := headers[name]; ok {
			t.Errorf("headers[%q] present, want it excluded (the reserved key must be stripped)", name)
		}
	}
}
