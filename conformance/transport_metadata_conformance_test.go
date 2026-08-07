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
// path. The version-carrying case is tier C (payload versioning) and is skipped with a log line;
// see conformance/README.md and ROADMAP.md.

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

	defaultKey := fixture.DefaultMetadataKeys.Topic

	for _, tc := range fixture.MetadataCases {
		t.Run(tc.Name, func(t *testing.T) {
			// benzene-version is tier C (payload versioning); this port does not implement it,
			// so the fixture instructs such ports to skip the case. See ROADMAP.md.
			if tc.Requires == "versioning" {
				t.Skipf("case requires %q; payload versioning is not implemented (tier C)", tc.Requires)
			}
			assertResolution(t, tc.Metadata, defaultKey, tc.Expected)
		})
	}

	for _, oc := range fixture.OverrideCases {
		t.Run(oc.Name, func(t *testing.T) {
			assertResolution(t, oc.Metadata, oc.MetadataKeys.Topic, oc.Expected)
		})
	}
}

func assertResolution(t *testing.T, metadata map[string]string, topicKey string, expected metadataExpectation) {
	t.Helper()
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)

	if topic != expected.Topic {
		t.Errorf("topic = %q, want %q", topic, expected.Topic)
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
