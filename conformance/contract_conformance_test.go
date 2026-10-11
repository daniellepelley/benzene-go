package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	declaredorders "github.com/daniellepelley/benzene-go/conformance/internal/naming/declared/orders"
	declaredpayments "github.com/daniellepelley/benzene-go/conformance/internal/naming/declared/payments"
	"github.com/daniellepelley/benzene-go/conformance/internal/naming/orders"
	"github.com/daniellepelley/benzene-go/conformance/internal/naming/payments"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/internal/jcs"
	"github.com/daniellepelley/benzene-go/wire"
)

// --- contract-document-cases.json (producer groups) and contract-hash-cases.json ---

// The canonical payload types (README.md "Canonical payload types"), under exactly the canonical
// names - here simply their Go names. No field has omitempty, so every property is required.

type GreetRequest struct {
	Name string `json:"name"`
}

type GreetResponse struct {
	Greeting string `json:"greeting"`
}

type Greeted struct {
	Greeting string `json:"greeting"`
}

type LogEntry struct {
	Message string `json:"message"`
}

type LookupRequest struct {
	Key string `json:"key"`
}

type LookupResponse struct {
	Value string `json:"value"`
}

type ShapesChild struct {
	Name string `json:"name"`
}

type ShapesRequest struct {
	Count         int               `json:"count"`
	Ratio         float64           `json:"ratio"`
	Active        bool              `json:"active"`
	Tags          []string          `json:"tags"`
	Labels        map[string]string `json:"labels"`
	Note          *string           `json:"note"`
	Child         ShapesChild       `json:"child"`
	NullableChild *ShapesChild      `json:"nullableChild"`
}

type TreeNode struct {
	Name     string     `json:"name"`
	Parent   *TreeNode  `json:"parent"`
	Children []TreeNode `json:"children"`
}

// noPayload is a handler response type with no payload: the unnamed empty struct.
type noPayload = struct{}

func handle[TReq, TRes any](_ context.Context, _ TReq) benzene.Result[TRes] {
	var zero TRes
	return benzene.Ok(zero)
}

type registrationEntry struct {
	Topic    string `json:"topic"`
	Version  string `json:"version"`
	Request  string `json:"request"`
	Response string `json:"response"`
	Message  string `json:"message"`
}

type registrations struct {
	Requests []registrationEntry `json:"requests"`
	Events   []registrationEntry `json:"events"`
	Sends    []registrationEntry `json:"sends"`
}

type contractDocumentFixture struct {
	DerivationCases []struct {
		Name          string          `json:"name"`
		Registrations registrations   `json:"registrations"`
		Expected      json.RawMessage `json:"expected"`
	} `json:"derivationCases"`
	NamingCases []struct {
		Name          string            `json:"name"`
		DeclaredNames map[string]string `json:"declaredNames"`
		Registrations registrations     `json:"registrations"`
		Expected      struct {
			Error          string   `json:"error"`
			Name           string   `json:"name"`
			ComponentNames []string `json:"componentNames"`
		} `json:"expected"`
	} `json:"namingCases"`
	SpecTopicCases []struct {
		Name     string          `json:"name"`
		Request  json.RawMessage `json:"request"`
		Expected struct {
			StatusCode string         `json:"statusCode"`
			Document   map[string]any `json:"document"`
		} `json:"expected"`
	} `json:"specTopicCases"`
	SpecHTTPCases []struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Expected struct {
			HTTPStatus int            `json:"httpStatus"`
			Document   map[string]any `json:"document"`
		} `json:"expected"`
	} `json:"specHttpCases"`
}

// registerCanonical registers the case's registrations natively, with no HTTP mappings. Each payload
// type name resolves to the canonical Go type; a pair the runner does not know fails the case.
func registerCanonical(t *testing.T, regs registrations) (*benzene.Registry, *contract.OutboundRegistry) {
	t.Helper()
	registry := benzene.NewRegistry()
	outbound := contract.NewOutboundRegistry()
	for _, r := range regs.Requests {
		topic := benzene.NewTopic(r.Topic).WithVersion(r.Version)
		switch r.Request + "->" + r.Response {
		case "GreetRequest->GreetResponse":
			must(t, benzene.Register(registry, topic, handle[GreetRequest, GreetResponse]))
		case "ShapesRequest->":
			must(t, benzene.Register(registry, topic, handle[ShapesRequest, noPayload]))
		case "TreeNode->":
			must(t, benzene.Register(registry, topic, handle[TreeNode, noPayload]))
		default:
			t.Fatalf("no canonical handler for %s -> %s", r.Request, r.Response)
		}
	}
	for _, e := range regs.Events {
		topic := benzene.NewTopic(e.Topic).WithVersion(e.Version)
		switch e.Message {
		case "Greeted":
			must(t, contract.RegisterEvent[Greeted](outbound, topic))
		default:
			t.Fatalf("no canonical event type %s", e.Message)
		}
	}
	for _, s := range regs.Sends {
		topic := benzene.NewTopic(s.Topic).WithVersion(s.Version)
		switch s.Request + "->" + s.Response {
		case "LogEntry->":
			must(t, contract.RegisterSend[LogEntry](outbound, topic))
		case "LookupRequest->LookupResponse":
			must(t, contract.RegisterSend[LookupRequest](outbound, topic, contract.WithResponse[LookupResponse]()))
		default:
			t.Fatalf("no canonical send for %s -> %s", s.Request, s.Response)
		}
	}
	return registry, outbound
}

func loadContractDocumentFixture(t *testing.T) contractDocumentFixture {
	t.Helper()
	var fixture contractDocumentFixture
	loadFixture(t, "contract-document-cases.json", &fixture)
	return fixture
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	out, err := jcs.Append(nil, v)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return string(out)
}

var componentKey = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func TestConformance_ContractDerivationCases(t *testing.T) {
	fixture := loadContractDocumentFixture(t)
	requireCases(t, len(fixture.DerivationCases), "contract-document-cases", "derivationCases")

	for _, c := range fixture.DerivationCases {
		t.Run(c.Name, func(t *testing.T) {
			registry, outbound := registerCanonical(t, c.Registrations)
			doc, err := contract.Derive(registry, outbound, contract.Info{Service: "conformance"})
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			derived := decodeJSON(t, raw)
			expected := decodeJSON(t, c.Expected)

			// Entries compare exactly after §6.2 steps 3-4 (the projection's own normalization), as
			// sets keyed by topic and version: Projection sorts them.
			got, want := contract.Projection(derived, true), contract.Projection(expected, true)
			for _, list := range []string{"requests", "events", "sends"} {
				if g, w := canonical(t, got[list]), canonical(t, want[list]); g != w {
					t.Errorf("%s:\n got  %s\n want %s", list, g, w)
				}
			}

			if doc.ContractHash != expected["contractHash"] {
				t.Errorf("contractHash = %s, want %v", doc.ContractHash, expected["contractHash"])
			}
			if doc.RegistrationHash != expected["registrationHash"] {
				t.Errorf("registrationHash = %s, want %v", doc.RegistrationHash, expected["registrationHash"])
			}

			// Every component key is valid and every $ref resolves.
			for name := range doc.Components.Schemas {
				if !componentKey.MatchString(name) {
					t.Errorf("component key %q is not valid", name)
				}
			}
			for _, ref := range refsIn(derived) {
				name, ok := strings.CutPrefix(ref, contract.RefPrefix)
				if _, found := doc.Components.Schemas[name]; !ok || !found {
					t.Errorf("$ref %q does not resolve", ref)
				}
			}
		})
	}
}

func refsIn(v any) []string {
	var refs []string
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if s, ok := child.(string); ok && k == "$ref" {
				refs = append(refs, s)
				continue
			}
			refs = append(refs, refsIn(child)...)
		}
	case []any:
		for _, child := range t {
			refs = append(refs, refsIn(child)...)
		}
	}
	return refs
}

func TestConformance_ContractNamingCases(t *testing.T) {
	fixture := loadContractDocumentFixture(t)
	requireCases(t, len(fixture.NamingCases), "contract-document-cases", "namingCases")

	for _, c := range fixture.NamingCases {
		t.Run(c.Name, func(t *testing.T) {
			// The fixture's qualified type names map onto Go packages; with declaredNames, onto the
			// same types declaring those names through BenzeneSchemaName.
			declaredFor := func(qualified string) string { return c.DeclaredNames[qualified] }
			registry := benzene.NewRegistry()
			var topics []string
			for _, r := range c.Registrations.Requests {
				topic := benzene.NewTopic(r.Topic)
				topics = append(topics, r.Topic)
				switch {
				case r.Request == "orders.Item" && declaredFor(r.Request) == "":
					must(t, benzene.Register(registry, topic, handle[orders.Item, noPayload]))
				case r.Request == "payments.Item" && declaredFor(r.Request) == "":
					must(t, benzene.Register(registry, topic, handle[payments.Item, noPayload]))
				case r.Request == "orders.Item" && declaredFor(r.Request) == (declaredorders.Item{}).BenzeneSchemaName():
					must(t, benzene.Register(registry, topic, handle[declaredorders.Item, noPayload]))
				case r.Request == "payments.Item" && declaredFor(r.Request) == (declaredpayments.Item{}).BenzeneSchemaName():
					must(t, benzene.Register(registry, topic, handle[declaredpayments.Item, noPayload]))
				default:
					t.Fatalf("no Go type for %s declared as %q", r.Request, declaredFor(r.Request))
				}
			}

			doc, err := contract.Derive(registry, nil, contract.Info{Service: "conformance"})
			if c.Expected.Error != "" {
				if c.Expected.Error != "component-name-collision" {
					t.Fatalf("unknown expected error %q", c.Expected.Error)
				}
				var collision *contract.NameCollisionError
				if !errors.As(err, &collision) {
					t.Fatalf("Derive error = %v, want a *contract.NameCollisionError", err)
				}
				if collision.Name != c.Expected.Name {
					t.Errorf("collision name = %q, want %q", collision.Name, c.Expected.Name)
				}
				if len(collision.Types) != 2 {
					t.Errorf("collision names types %v, want both", collision.Types)
				}
				sort.Strings(topics)
				if strings.Join(collision.Topics, ",") != strings.Join(topics, ",") {
					t.Errorf("collision names topics %v, want %v", collision.Topics, topics)
				}
				return
			}
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			for _, name := range c.Expected.ComponentNames {
				if _, ok := doc.Components.Schemas[name]; !ok {
					t.Errorf("components lack %q: %v", name, keysOf(doc.Components.Schemas))
				}
			}
		})
	}
}

func keysOf(m map[string]contract.Schema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// canonicalSpecBuilder serves the canonical-service document on benzene:spec, ahead of the router.
func canonicalSpecBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry, outbound := registerCanonical(t, registrations{
		Requests: []registrationEntry{{Topic: "conformance:greet", Request: "GreetRequest", Response: "GreetResponse"}},
		Events:   []registrationEntry{{Topic: "conformance:greeted", Message: "Greeted"}},
		Sends:    []registrationEntry{{Topic: "conformance:log", Request: "LogEntry"}},
	})
	doc, err := contract.Derive(registry, outbound, contract.Info{Service: "conformance"})
	if err != nil {
		t.Fatal(err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(contract.Middleware(doc), benzene.RouterMiddleware(registry)),
	}
}

func TestConformance_ContractSpecTopicCases(t *testing.T) {
	fixture := loadContractDocumentFixture(t)
	requireCases(t, len(fixture.SpecTopicCases), "contract-document-cases", "specTopicCases")
	builder := canonicalSpecBuilder(t)

	for _, c := range fixture.SpecTopicCases {
		t.Run(c.Name, func(t *testing.T) {
			body := string(c.Request)
			if body == "null" || body == "" {
				body = "" // null means an absent body
			}
			resp := envelope.Dispatch(context.Background(), builder.Pipeline, builder.Container, wire.Request{Topic: contract.TopicID, Body: body})
			if resp.StatusCode != c.Expected.StatusCode {
				t.Fatalf("statusCode = %q, want %q (body %s)", resp.StatusCode, c.Expected.StatusCode, resp.Body)
			}
			if c.Expected.Document != nil {
				var document map[string]any
				if err := json.Unmarshal([]byte(resp.Body), &document); err != nil {
					t.Fatalf("response body is not a document: %v", err)
				}
				for _, m := range subsetMismatches(c.Expected.Document, document) {
					t.Error(m)
				}
			}
		})
	}
}

func TestConformance_ContractSpecHTTPCases(t *testing.T) {
	fixture := loadContractDocumentFixture(t)
	requireCases(t, len(fixture.SpecHTTPCases), "contract-document-cases", "specHttpCases")
	server := httptest.NewServer(httpbinding.SpecHandler(canonicalSpecBuilder(t)))
	defer server.Close()

	for _, c := range fixture.SpecHTTPCases {
		t.Run(c.Name, func(t *testing.T) {
			resp, err := http.Get(server.URL + c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.Expected.HTTPStatus {
				t.Fatalf("HTTP status = %d, want %d", resp.StatusCode, c.Expected.HTTPStatus)
			}
			if c.Expected.Document != nil {
				var document map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&document); err != nil {
					t.Fatalf("response body is not a document: %v", err)
				}
				for _, m := range subsetMismatches(c.Expected.Document, document) {
					t.Error(m)
				}
			}
		})
	}
}

type contractHashFixture struct {
	Cases []struct {
		Name                     string          `json:"name"`
		Document                 json.RawMessage `json:"document"`
		Projection               json.RawMessage `json:"projection"`
		ExpectedContractHash     string          `json:"expectedContractHash"`
		ExpectedRegistrationHash string          `json:"expectedRegistrationHash"`
	} `json:"cases"`
}

// A contract-document producer runs every hash case without a projection, through the runtime hash
// its documents carry. The projection cases are a client generator's, and run in codegen's runner.
func TestConformance_ContractHashCases(t *testing.T) {
	var fixture contractHashFixture
	loadFixture(t, "contract-hash-cases.json", &fixture)
	requireCases(t, len(fixture.Cases), "contract-hash-cases", "cases")

	ran := 0
	for _, c := range fixture.Cases {
		if len(c.Projection) > 0 {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			contractHash, registrationHash, err := contract.HashDocument(c.Document)
			if err != nil {
				t.Fatal(err)
			}
			if contractHash != c.ExpectedContractHash {
				t.Errorf("contractHash = %s, want %s", contractHash, c.ExpectedContractHash)
			}
			if registrationHash != c.ExpectedRegistrationHash {
				t.Errorf("registrationHash = %s, want %s", registrationHash, c.ExpectedRegistrationHash)
			}
		})
	}
	requireCases(t, ran, "contract-hash-cases", "cases without a projection")
}
