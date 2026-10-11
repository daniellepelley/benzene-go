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
// path. The version cases run unconditionally (benzene-version is tier A): the version travels
// as a header alongside the topic (the fixture's headersExclude lists only the topic key, not
// the version key), read by wire.ResolveVersion under the port's own default key the same way
// RouterMiddleware reads it off ic.Headers downstream - and a bare "version" key stays a header
// that selects nothing (D10, no fallback list).

type transportMetadataFixture struct {
	DefaultMetadataKeys struct {
		Topic   string `json:"topic"`
		Version string `json:"version"`
		Headers string `json:"headers"`
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

	// The port's own constants, compared against the fixture's names - the cheap check that a
	// renamed or misspelled default cannot pass. The resolution below then runs under the port's
	// constants, not the fixture's, so it exercises what a service actually ships with.
	if wire.DefaultTopicKey != fixture.DefaultMetadataKeys.Topic {
		t.Errorf("wire.DefaultTopicKey = %q, want %q", wire.DefaultTopicKey, fixture.DefaultMetadataKeys.Topic)
	}
	if wire.DefaultVersionKey != fixture.DefaultMetadataKeys.Version {
		t.Errorf("wire.DefaultVersionKey = %q, want %q", wire.DefaultVersionKey, fixture.DefaultMetadataKeys.Version)
	}
	// The EventBridge embedded-headers key (awseventbridge.EmbeddedHeadersKey is this constant).
	if wire.EmbeddedHeadersKey != fixture.DefaultMetadataKeys.Headers {
		t.Errorf("wire.EmbeddedHeadersKey = %q, want %q", wire.EmbeddedHeadersKey, fixture.DefaultMetadataKeys.Headers)
	}
	var names wire.ReservedNames // the zero value is the defaults a service gets untouched
	defaultKey := names.Topic()
	versionKey := names.Version()

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
	// case without a benzene-version key, which ResolveVersion also yields then.
	if version := wire.ResolveVersion(headers, versionKey); version != expected.Version {
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
