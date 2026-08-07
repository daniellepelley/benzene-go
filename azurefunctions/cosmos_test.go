package azurefunctions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// changeDoc is one document as it arrives in a change-feed batch.
type changeDoc struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// newCosmosBuilder wires a service whose "orders:changed" handler receives the whole batch as a
// slice - the fan-in shape (core-concepts.md §3): one invocation for the batch, not one per
// document. It records the batches it saw so a test can assert the fan-in delivered them intact.
func newCosmosBuilder(t *testing.T, seen *[][]changeDoc) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("orders:changed"), benzene.Handler[[]changeDoc, struct{}](
		func(_ context.Context, batch []changeDoc) benzene.Result[struct{}] {
			if seen != nil {
				*seen = append(*seen, batch)
			}
			for _, d := range batch {
				if d.ID == "" {
					return benzene.BadRequest[struct{}]("document id is required")
				}
			}
			return benzene.Ok(struct{}{})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func invokeCosmos(t *testing.T, handler http.Handler, hostPayload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/OrdersChanged", strings.NewReader(hostPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCosmosHandler_BatchAsArrayFansInAsOneInvocation(t *testing.T) {
	var seen [][]changeDoc
	handler := CosmosHandler(newCosmosBuilder(t, &seen), benzene.NewTopic("orders:changed"), "documents")

	// The host delivers the batch as a JSON array directly under the binding name.
	hostPayload := `{"Data": {"documents": [{"id":"o-1","value":"a"},{"id":"o-2","value":"b"}]}}`

	rec := invokeCosmos(t, handler, hostPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 1 {
		t.Fatalf("pipeline ran %d times, want exactly 1 (one invocation per batch, not per item)", len(seen))
	}
	if len(seen[0]) != 2 || seen[0][0].ID != "o-1" || seen[0][1].ID != "o-2" {
		t.Errorf("batch = %+v, want the two documents intact", seen[0])
	}
	var resp queueSuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	if resp.Outputs == nil {
		t.Error("Outputs should be present (empty object), not absent")
	}
}

func TestCosmosHandler_BatchAsStringWrappedArrayIsUnwrapped(t *testing.T) {
	// The host may serialize the trigger input as a JSON *string* wrapping the array (the same
	// double-encoding queue triggers use); it must be unwrapped so the handler sees []changeDoc.
	var seen [][]changeDoc
	handler := CosmosHandler(newCosmosBuilder(t, &seen), benzene.NewTopic("orders:changed"), "documents")

	hostPayload := `{"Data": {"documents": "[{\"id\":\"o-9\",\"value\":\"z\"}]"}}`

	rec := invokeCosmos(t, handler, hostPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 1 || len(seen[0]) != 1 || seen[0][0].ID != "o-9" {
		t.Errorf("batch = %+v, want the single unwrapped document", seen)
	}
}

func TestCosmosHandler_FailedBatchIsOuterFailureForRedelivery(t *testing.T) {
	// A non-success dispatch answers outer HTTP 500: the host does not checkpoint, so it
	// redelivers the *whole* batch (batch-level checkpoint, no per-document resume).
	handler := CosmosHandler(newCosmosBuilder(t, nil), benzene.NewTopic("orders:changed"), "documents")

	rec := invokeCosmos(t, handler, `{"Data": {"documents": [{"id":"o-1","value":"a"},{"value":"no-id"}]}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var payload wire.ErrorPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(error payload) error = %v; body = %s", err, rec.Body.String())
	}
	if payload.Status == "" {
		t.Errorf("error payload Status is empty; body = %s", rec.Body.String())
	}
}

func TestCosmosHandler_AbsentBindingNameIsEmptyBatch(t *testing.T) {
	// The host never fires this trigger without changes, but an absent binding name degrades to
	// an empty batch (a no-op that checkpoints) rather than a failure.
	var seen [][]changeDoc
	handler := CosmosHandler(newCosmosBuilder(t, &seen), benzene.NewTopic("orders:changed"), "documents")

	rec := invokeCosmos(t, handler, `{"Data": {}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 1 || len(seen[0]) != 0 {
		t.Errorf("batch = %+v, want a single empty batch", seen)
	}
}

func TestCosmosHandler_VersionedTopicFansInToVersionedHandler(t *testing.T) {
	// A fan-in binding names its topic in code, so it can target a *versioned* handler by explicit
	// programmatic dispatch (DispatchTopicResult) - distinct from reading a version header off an
	// inbound message, which no binding does.
	registry := benzene.NewRegistry()
	unversioned, versioned := false, false
	if err := benzene.Register(registry, benzene.NewTopic("orders:changed"), benzene.Handler[[]changeDoc, struct{}](
		func(context.Context, []changeDoc) benzene.Result[struct{}] {
			unversioned = true
			return benzene.Ok(struct{}{})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := benzene.Register(registry, benzene.NewTopic("orders:changed").WithVersion("2"), benzene.Handler[[]changeDoc, struct{}](
		func(context.Context, []changeDoc) benzene.Result[struct{}] {
			versioned = true
			return benzene.Ok(struct{}{})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	handler := CosmosHandler(builder, benzene.NewTopic("orders:changed").WithVersion("2"), "documents")

	rec := invokeCosmos(t, handler, `{"Data": {"documents": [{"id":"o-1","value":"a"}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !versioned || unversioned {
		t.Errorf("versioned handler ran = %v, unversioned ran = %v; want only the versioned handler", versioned, unversioned)
	}
}

func TestCosmosHandler_MalformedHostPayloadIsBadRequest(t *testing.T) {
	handler := CosmosHandler(newCosmosBuilder(t, nil), benzene.NewTopic("orders:changed"), "documents")
	rec := invokeCosmos(t, handler, "{not valid json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCosmosHandler_BodyReadErrorIsOuterBadRequest(t *testing.T) {
	handler := CosmosHandler(newCosmosBuilder(t, nil), benzene.NewTopic("orders:changed"), "documents")

	req := httptest.NewRequest(http.MethodPost, "/OrdersChanged", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestResolveCosmosBatch(t *testing.T) {
	tests := []struct {
		name      string
		inv       cosmosInvocationRequest
		dataName  string
		wantBody  string
		wantCount string // "" means the header must be absent
	}{
		{
			name:      "array delivered directly",
			inv:       cosmosInvocationRequest{Data: map[string]json.RawMessage{"documents": json.RawMessage(`[{"id":"a"}]`)}},
			dataName:  "documents",
			wantBody:  `[{"id":"a"}]`,
			wantCount: "1",
		},
		{
			name:      "string-wrapped array is unwrapped",
			inv:       cosmosInvocationRequest{Data: map[string]json.RawMessage{"documents": json.RawMessage(`"[{\"id\":\"a\"},{\"id\":\"b\"}]"`)}},
			dataName:  "documents",
			wantBody:  `[{"id":"a"},{"id":"b"}]`,
			wantCount: "2",
		},
		{
			name:      "absent binding name is empty batch",
			inv:       cosmosInvocationRequest{Data: map[string]json.RawMessage{}},
			dataName:  "documents",
			wantBody:  `[]`,
			wantCount: "0",
		},
		{
			name:      "non-array body omits the count header",
			inv:       cosmosInvocationRequest{Data: map[string]json.RawMessage{"documents": json.RawMessage(`{"not":"an array"}`)}},
			dataName:  "documents",
			wantBody:  `{"not":"an array"}`,
			wantCount: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, body := resolveCosmosBatch(tt.inv, tt.dataName)
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
			if got := headers["cosmos-document-count"]; got != tt.wantCount {
				t.Errorf("cosmos-document-count = %q, want %q", got, tt.wantCount)
			}
		})
	}
}
