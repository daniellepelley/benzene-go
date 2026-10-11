// Package transportmetadata runs transport-metadata-cases.json's metadataCases and overrideCases
// against a real inbound binding. The fixture's cases are written against a neutral string->string
// metadata dictionary, and "each runner adapts them to its own native shape": a binding test
// supplies a Probe that puts the case's metadata on its transport's native channel (SQS message
// attributes, Service Bus application properties, AMQP headers, ...), configures the binding the way
// a service would - the reserved names set once on the builder - and delivers one message. What
// reached the pipeline is then held to the case's expectation.
//
// It lives under the root module's internal/ so the binding modules (awssqs, rabbitmq, ...) can
// import it as well as the root conformance runner: each binding runs the same cases through its
// own entry point, rather than the runner exercising only the shared resolution primitive and
// trusting every binding to call it.
package transportmetadata

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// FixtureName is the fixture these cases come from.
const FixtureName = "transport-metadata-cases.json"

// Topic is the topic every routing case resolves to; the recording builder registers a default
// handler and a version "2" handler for it, so a case's expected version is observed as which
// handler the real router selected, not computed beside it.
const Topic = "conformance:greet"

// Body is the message body a probe delivers. It is a JSON object that is not a wire envelope, so a
// binding's envelope-in-body fallback (taken only when the metadata resolves no topic) finds no topic
// in it and the fixture's "unresolved" cases stay unresolved.
const Body = `{"name":"conformance"}`

// Expectation is one case's expected resolution.
type Expectation struct {
	Topic          string            `json:"topic"`
	Version        string            `json:"version"`
	Headers        map[string]string `json:"headers"`
	HeadersExclude []string          `json:"headersExclude"`
}

// Case is one metadataCases or overrideCases entry. TopicKey is empty for a metadataCases entry
// (the defaults) and the overridden key for an overrideCases entry.
type Case struct {
	Name     string
	TopicKey string
	Metadata map[string]string
	Expected Expectation
}

// Names is the reserved names a service configures for this case.
func (c Case) Names() wire.ReservedNames { return wire.ReservedNames{TopicKey: c.TopicKey} }

type fixture struct {
	MetadataCases []struct {
		Name     string            `json:"name"`
		Metadata map[string]string `json:"metadata"`
		Expected Expectation       `json:"expected"`
	} `json:"metadataCases"`
	OverrideCases []struct {
		Name         string                 `json:"name"`
		MetadataKeys struct{ Topic string } `json:"metadataKeys"`
		Metadata     map[string]string      `json:"metadata"`
		Expected     Expectation            `json:"expected"`
	} `json:"overrideCases"`
}

// Load reads the fixture at path and returns its metadataCases followed by its overrideCases. It
// fails the test when either list is empty: json.Unmarshal leaves a renamed key's slice nil, and a
// run that checked nothing must not read as a pass.
func Load(t testing.TB, path string) []Case {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(f.MetadataCases) == 0 || len(f.OverrideCases) == 0 {
		t.Fatalf("%s: metadataCases (%d) or overrideCases (%d) is empty - the runner and the fixture have drifted",
			path, len(f.MetadataCases), len(f.OverrideCases))
	}
	cases := make([]Case, 0, len(f.MetadataCases)+len(f.OverrideCases))
	for _, c := range f.MetadataCases {
		cases = append(cases, Case{Name: c.Name, Metadata: c.Metadata, Expected: c.Expected})
	}
	for _, c := range f.OverrideCases {
		if c.MetadataKeys.Topic == "" {
			t.Fatalf("%s: overrideCases %q overrides no topic key", path, c.Name)
		}
		cases = append(cases, Case{Name: c.Name, TopicKey: c.MetadataKeys.Topic, Metadata: c.Metadata, Expected: c.Expected})
	}
	return cases
}

// Delivery is what one message put in front of the pipeline: the resolved topic and headers, and
// the version of the handler the router selected ("" for the default handler, or when none ran).
type Delivery struct {
	Topic   string
	Headers map[string]string
	Version string
}

// Probe delivers one message through a binding. builder comes from NewBuilder, already configured
// with the case's reserved names; metadata is what the message carries on the transport's native
// metadata channel, and its body is Body. The probe returns once the binding has dispatched it.
type Probe func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string)

// Run runs every case in the fixture at path through probe.
func Run(t *testing.T, path string, probe Probe) {
	t.Helper()
	for _, c := range Load(t, path) {
		t.Run(c.Name, func(t *testing.T) {
			builder, recorder := NewBuilder(t, c.Names())
			metadata := make(map[string]string, len(c.Metadata))
			for k, v := range c.Metadata {
				metadata[k] = v
			}
			probe(t, builder, metadata)
			Assert(t, recorder.Only(t), c.Expected)
		})
	}
}

// Assert holds one delivery to a case's expectation.
func Assert(t *testing.T, got Delivery, expected Expectation) {
	t.Helper()
	if got.Topic != expected.Topic {
		t.Errorf("topic = %q, want %q", got.Topic, expected.Topic)
	}
	if got.Version != expected.Version {
		t.Errorf("version = %q (the handler the router selected), want %q", got.Version, expected.Version)
	}
	for name, want := range expected.Headers {
		if value, ok := got.Headers[name]; !ok || value != want {
			t.Errorf("headers[%q] = %q (present=%v), want %q; headers = %v", name, value, ok, want, got.Headers)
		}
	}
	for _, name := range expected.HeadersExclude {
		if _, ok := got.Headers[name]; ok {
			t.Errorf("headers[%q] present, want it excluded (the consumed topic key must not reappear as a header); headers = %v", name, got.Headers)
		}
	}
}

// Recorder records every invocation a builder made by NewBuilder dispatches.
type Recorder struct {
	mu         sync.Mutex
	deliveries []*Delivery
}

// Only returns the single delivery recorded, failing the test when there was not exactly one.
func (r *Recorder) Only(t testing.TB) Delivery {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.deliveries) != 1 {
		t.Fatalf("the binding dispatched %d invocations, want exactly 1", len(r.deliveries))
	}
	return *r.deliveries[0]
}

type versionKey struct{}

// NewBuilder returns a builder configured with names (via UseReservedNames, as a service would)
// whose pipeline records what each invocation carried and then runs the real router, reading the
// version under names.Version(). Topic has a default handler and a version "2" handler; both answer
// ok, so a delivery that resolves is acknowledged.
func NewBuilder(t testing.TB, names wire.ReservedNames) (*benzene.ApplicationBuilder, *Recorder) {
	t.Helper()
	recorder := &Recorder{}
	builder := benzene.App[struct{}]{}.Run()
	builder.UseReservedNames(names)

	for _, version := range []string{"", "2"} {
		version := version
		handler := func(ctx context.Context, _ any) benzene.Result[struct{}] {
			if delivery, ok := ctx.Value(versionKey{}).(*Delivery); ok {
				delivery.Version = version
			}
			return benzene.Ok(struct{}{})
		}
		if err := benzene.Register(builder.Registry, benzene.NewTopic(Topic).WithVersion(version), handler); err != nil {
			t.Fatalf("register %s@%s: %v", Topic, version, err)
		}
	}

	record := func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		delivery := &Delivery{Topic: ic.Topic.ID, Headers: make(map[string]string, len(ic.Headers))}
		for k, v := range ic.Headers {
			delivery.Headers[k] = v
		}
		recorder.mu.Lock()
		recorder.deliveries = append(recorder.deliveries, delivery)
		recorder.mu.Unlock()
		return next(context.WithValue(ctx, versionKey{}, delivery))
	}
	builder.UsePipeline(benzene.NewPipeline(record,
		benzene.RouterMiddleware(builder.Registry, benzene.WithVersionKey(names.Version()))))
	return builder, recorder
}
