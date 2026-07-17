package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
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

// capture runs one invocation through Middleware (outermost, ahead of the router) with a
// JSON slog handler and decodes the single line it logs.
func capture(t *testing.T, request any, extraMiddleware ...benzene.Middleware) map[string]any {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet").WithVersion("2"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	middleware := append([]benzene.Middleware{Middleware(logger)}, extraMiddleware...)
	middleware = append(middleware, benzene.RouterMiddleware(registry))
	pipeline := benzene.NewPipeline(middleware...)

	ic := benzene.NewInvocationContext(benzene.NewTopic("greet").WithVersion("2"), nil, request, nil)
	_ = pipeline.Run(context.Background(), ic)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("middleware logged nothing")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("json.Unmarshal(log line) error = %v; line = %s", err, line)
	}
	return record
}

func TestMiddleware_SuccessLogsInfoWithTopicStatusDuration(t *testing.T) {
	record := capture(t, greetRequest{Name: "World"})

	if record["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", record["level"])
	}
	if record["msg"] != "invocation completed" {
		t.Errorf("msg = %v, want %q", record["msg"], "invocation completed")
	}
	if record["topic"] != "greet" {
		t.Errorf("topic = %v, want greet", record["topic"])
	}
	if record["topic_version"] != "2" {
		t.Errorf("topic_version = %v, want 2", record["topic_version"])
	}
	if record["status"] != string(benzene.StatusOk) {
		t.Errorf("status = %v, want %q", record["status"], benzene.StatusOk)
	}
	if _, ok := record["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %v (%T), want a number", record["duration_ms"], record["duration_ms"])
	}
}

func TestMiddleware_FailureResultLogsWarnWithErrors(t *testing.T) {
	record := capture(t, greetRequest{Name: ""})

	if record["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", record["level"])
	}
	if record["status"] != string(benzene.StatusBadRequest) {
		t.Errorf("status = %v, want %q", record["status"], benzene.StatusBadRequest)
	}
	errorsAttr, _ := record["errors"].(string)
	if !strings.Contains(errorsAttr, "name is required") {
		t.Errorf("errors = %q, want the result's error detail", errorsAttr)
	}
}

func TestMiddleware_PipelineErrorLogsErrorAndPropagates(t *testing.T) {
	boom := errors.New("middleware exploded")
	failing := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		return boom
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	pipeline := benzene.NewPipeline(Middleware(logger), failing)

	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), nil, nil, nil)
	if err := pipeline.Run(context.Background(), ic); !errors.Is(err, boom) {
		t.Fatalf("Run() error = %v, want the propagated middleware error", err)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record); err != nil {
		t.Fatalf("json.Unmarshal(log line) error = %v", err)
	}
	if record["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", record["level"])
	}
	if record["msg"] != "invocation failed" {
		t.Errorf("msg = %v, want %q", record["msg"], "invocation failed")
	}
	if record["error"] != "middleware exploded" {
		t.Errorf("error = %v, want the propagated error text", record["error"])
	}
}

func TestMiddleware_NoResultLogsWarnWithEmptyStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	// No router - nothing downstream produces a Result.
	pipeline := benzene.NewPipeline(Middleware(logger))

	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), nil, nil, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record); err != nil {
		t.Fatalf("json.Unmarshal(log line) error = %v", err)
	}
	if record["level"] != "WARN" {
		t.Errorf("level = %v, want WARN for a resultless invocation", record["level"])
	}
	if record["status"] != "" {
		t.Errorf("status = %v, want empty", record["status"])
	}
	if _, ok := record["errors"]; ok {
		t.Error(`"errors" should be absent when there is no Result to read them from`)
	}
}

func TestMiddleware_NilLoggerUsesDefault(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	pipeline := benzene.NewPipeline(Middleware(nil), benzene.RouterMiddleware(registry))

	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), nil, greetRequest{Name: "World"}, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(buf.String(), "invocation completed") {
		t.Errorf("default logger captured %q, want the invocation line", buf.String())
	}
}
