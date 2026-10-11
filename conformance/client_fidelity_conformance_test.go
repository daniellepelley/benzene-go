package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/inprocess"
	"github.com/daniellepelley/benzene-go/wire"
)

// core-concepts.md §12, error fidelity (Core): a client that receives a problem document - the
// body of a failure envelope, or an application/problem+json response - MUST rebuild the result's
// errors from it with field and code intact. problem-details-cases.json's envelopeCases pin what the
// serving side puts in that document; this runs the same cases end to end through every outbound
// client in this module that can receive one, and holds the Result the caller gets back to the
// case's expected status and errors.
//
// awslambdaclient runs the same cases in its own module (this one cannot import it). The one-way
// clients (awssqs, awssns, awseventbridge, gcppubsubclient, azureservicebus, azureeventhub,
// azureeventgrid, azurequeuestorage, rabbitmq, kafka, awsstepfunctions) receive no reply, so no
// problem document, and answer accepted; grpcbinding.Client receives the gRPC error model of
// wire-contracts.md §4.2, not a problem document, and its field recovery is tested in its module.

// fidelityClient is one outbound client, aimed at a service built from the canonical handlers.
type fidelityClient struct {
	name string
	new  func(t *testing.T, builder *benzene.ApplicationBuilder) client.Sender
}

func fidelityClients() []fidelityClient {
	return []fidelityClient{
		{"httpclient over the envelope endpoint", func(t *testing.T, builder *benzene.ApplicationBuilder) client.Sender {
			server := httptest.NewServer(httpbinding.EnvelopeHandler(builder))
			t.Cleanup(server.Close)
			return httpclient.NewClient(server.URL)
		}},
		// The problem arrives as the application/problem+json body of a route-mapped binding, not
		// inside an envelope: a front door that maps the client's envelope onto a native route.
		{"httpclient over a route-mapped problem+json response", func(t *testing.T, builder *benzene.ApplicationBuilder) client.Sender {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				req, err := wire.UnmarshalRequest(data)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/route", Topic: benzene.NewTopic(req.Topic)}}
				httpbinding.Handler(builder, routes).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/route", strings.NewReader(req.Body)))
			}))
			t.Cleanup(server.Close)
			return httpclient.NewClient(server.URL)
		}},
		{"inprocess.Sender", func(t *testing.T, builder *benzene.ApplicationBuilder) client.Sender {
			set := inprocess.NewPipelineSet()
			must(t, set.Add("conformance", builder))
			sender, err := inprocess.NewSender(set, "conformance")
			must(t, err)
			return sender
		}},
	}
}

func TestConformance_ProblemClientErrorFidelity(t *testing.T) {
	fixture := loadProblemFixture(t)
	var cases []envelopeCase
	if err := json.Unmarshal(fixture.EnvelopeCases, &cases); err != nil {
		t.Fatalf("problem-details-cases: parse envelopeCases: %v", err)
	}
	requireCases(t, len(cases), "problem-details-cases", "envelopeCases")

	for _, fc := range fidelityClients() {
		t.Run(fc.name, func(t *testing.T) {
			sender := fc.new(t, conformanceBuilder(t))
			for _, c := range cases {
				t.Run(c.Name, func(t *testing.T) {
					if c.Requires != "" && !supportedCapabilities[c.Requires] {
						t.Skipf("requires %q, which this port does not implement (conformance/README.md)", c.Requires)
					}
					result := sender.Send(context.Background(), benzene.NewTopic(c.Request.Topic), c.Request.Headers, []byte(c.Request.Body))
					checkClientResult(t, result, c.Expected)
				})
			}
		})
	}
}

// checkClientResult holds a client's Result to an envelope case's expectation: the status, and -
// where the case states them - the errors (message, field and code, in order) or the set of
// errors[].field values.
func checkClientResult(t *testing.T, result benzene.Result[json.RawMessage], expected envelopeExpectation) {
	t.Helper()
	if string(result.Status) != expected.StatusCode {
		t.Errorf("status = %q, want %q", result.Status, expected.StatusCode)
	}
	if expected.IsSuccessful != nil && result.IsSuccessful() != *expected.IsSuccessful {
		t.Errorf("IsSuccessful() = %v, want %v", result.IsSuccessful(), *expected.IsSuccessful)
	}

	if raw, ok := expected.Body["errors"]; ok {
		data, err := json.Marshal(raw)
		must(t, err)
		var want []benzene.Error
		must(t, json.Unmarshal(data, &want))
		if len(result.Errors) != len(want) {
			t.Fatalf("errors = %+v, want %+v", result.Errors, want)
		}
		for i := range want {
			if result.Errors[i] != want[i] {
				t.Errorf("errors[%d] = %+v, want %+v (message, field and code intact)", i, result.Errors[i], want[i])
			}
		}
	}

	if expected.ErrorFields != nil {
		fields := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			fields = append(fields, e.Field)
		}
		want := append([]string(nil), expected.ErrorFields...)
		sort.Strings(fields)
		sort.Strings(want)
		if strings.Join(fields, ",") != strings.Join(want, ",") {
			t.Errorf("errors[].field = %v, want %v", fields, want)
		}
	}
}
