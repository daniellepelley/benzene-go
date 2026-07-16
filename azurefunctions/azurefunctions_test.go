package azurefunctions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func testRoutes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/Greet", Topic: benzene.NewTopic("greet")}}
}

func invoke(t *testing.T, handler http.Handler, localPath, method, name string) invocationResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"Data": map[string]any{
			"req": map[string]any{
				"Method":  method,
				"Headers": map[string]string{},
				"Body":    `{"name":"` + name + `"}`,
			},
		},
		"Metadata": map[string]any{},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var inv invocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	return inv
}

func TestHandler_MatchedRouteReturnsGreeting(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	inv := invoke(t, handler, "/Greet", "POST", "World")

	res, ok := inv.Outputs["res"]
	if !ok {
		t.Fatalf("Outputs missing \"res\": %+v", inv.Outputs)
	}
	if res.StatusCode != "200" {
		t.Fatalf("res.StatusCode = %q, want %q; body = %s", res.StatusCode, "200", res.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(res.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(res.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestHandler_FailureStatusMapsToNativeHTTPCodeInOutputs(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	inv := invoke(t, handler, "/Greet", "POST", "")

	res := inv.Outputs["res"]
	if res.StatusCode != "400" {
		t.Errorf("res.StatusCode = %q, want %q", res.StatusCode, "400")
	}
}

func TestHandler_UnmatchedRouteIsNotFoundInOutputs(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	inv := invoke(t, handler, "/NoSuchFunction", "POST", "World")

	res := inv.Outputs["res"]
	if res.StatusCode != "404" {
		t.Errorf("res.StatusCode = %q, want %q", res.StatusCode, "404")
	}
}

func TestHandler_MissingReqDataIsNotFound(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	body := `{"Data":{},"Metadata":{}}`
	req := httptest.NewRequest(http.MethodPost, "/Greet", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d", rec.Code, http.StatusOK)
	}
	var inv invocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if inv.Outputs["res"].StatusCode != "404" {
		t.Errorf(`res.StatusCode = %q, want "404" (empty Method never matches a route)`, inv.Outputs["res"].StatusCode)
	}
}

func TestHandler_MalformedInvocationPayloadIsOuterBadRequest(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	req := httptest.NewRequest(http.MethodPost, "/Greet", strings.NewReader("{not valid"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_MalformedReqDataIsOuterBadRequest(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	body := `{"Data":{"req":"not-an-object"},"Metadata":{}}`
	req := httptest.NewRequest(http.MethodPost, "/Greet", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestHandler_BodyReadErrorIsOuterBadRequest(t *testing.T) {
	handler := Handler(newTestBuilder(t), testRoutes())

	req := httptest.NewRequest(http.MethodPost, "/Greet", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
