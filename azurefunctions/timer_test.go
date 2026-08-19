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

// timerTick is the schedule info a handler can receive as its request.
type timerTick struct {
	IsPastDue bool `json:"IsPastDue"`
}

// newTimerBuilder wires a service whose "nightly-cleanup" handler receives the tick's schedule info,
// recording what it saw and optionally failing.
func newTimerBuilder(t *testing.T, seen *timerTick, fail bool) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("nightly-cleanup"), benzene.Handler[timerTick, struct{}](
		func(_ context.Context, tick timerTick) benzene.Result[struct{}] {
			if seen != nil {
				*seen = tick
			}
			if fail {
				return benzene.ServiceUnavailable[struct{}]("cleanup failed")
			}
			return benzene.Ok(struct{}{})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func invokeTimer(t *testing.T, handler http.Handler, hostPayload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/NightlyCleanup", strings.NewReader(hostPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTimerHandler_TickDispatchesWithScheduleInfo(t *testing.T) {
	var seen timerTick
	handler := TimerHandler(newTimerBuilder(t, &seen, false), benzene.NewTopic("nightly-cleanup"), "myTimer")

	rec := invokeTimer(t, handler, `{"Data": {"myTimer": {"IsPastDue": true}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !seen.IsPastDue {
		t.Error("handler did not receive the tick's schedule info (IsPastDue)")
	}
	var resp queueSuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	if resp.Outputs == nil {
		t.Error("Outputs should be present (empty object)")
	}
}

func TestTimerHandler_StringWrappedTickIsUnwrapped(t *testing.T) {
	var seen timerTick
	handler := TimerHandler(newTimerBuilder(t, &seen, false), benzene.NewTopic("nightly-cleanup"), "myTimer")

	rec := invokeTimer(t, handler, `{"Data": {"myTimer": "{\"IsPastDue\": true}"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !seen.IsPastDue {
		t.Error("string-wrapped tick was not unwrapped")
	}
}

func TestTimerHandler_EmptyRequestHandlerBindsWithNoTimerData(t *testing.T) {
	// A handler that ignores the schedule (empty request) must bind cleanly when the tick is absent,
	// null, or empty - the body defaults to "{}".
	registry := benzene.NewRegistry()
	ran := false
	if err := benzene.Register(registry, benzene.NewTopic("nightly-cleanup"), benzene.Handler[struct{}, struct{}](
		func(context.Context, struct{}) benzene.Result[struct{}] { ran = true; return benzene.Ok(struct{}{}) })); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	handler := TimerHandler(builder, benzene.NewTopic("nightly-cleanup"), "myTimer")

	for _, payload := range []string{`{"Data": {}}`, `{"Data": {"myTimer": null}}`} {
		ran = false
		rec := invokeTimer(t, handler, payload)
		if rec.Code != http.StatusOK {
			t.Errorf("outer status = %d for %s, want 200", rec.Code, payload)
		}
		if !ran {
			t.Errorf("handler did not run for %s", payload)
		}
	}
}

func TestTimerHandler_FailedTickIsOuterFailure(t *testing.T) {
	handler := TimerHandler(newTimerBuilder(t, nil, true), benzene.NewTopic("nightly-cleanup"), "myTimer")

	rec := invokeTimer(t, handler, `{"Data": {"myTimer": {"IsPastDue": false}}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var payload wire.ErrorPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v; body = %s", err, rec.Body.String())
	}
	if payload.BenzeneStatus == "" {
		t.Errorf("error payload benzeneStatus is empty; body = %s", rec.Body.String())
	}
}

func TestTimerHandler_MalformedHostPayloadIsBadRequest(t *testing.T) {
	handler := TimerHandler(newTimerBuilder(t, nil, false), benzene.NewTopic("nightly-cleanup"), "myTimer")
	rec := invokeTimer(t, handler, "{not valid json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestTimerHandler_BodyReadErrorIsBadRequest(t *testing.T) {
	handler := TimerHandler(newTimerBuilder(t, nil, false), benzene.NewTopic("nightly-cleanup"), "myTimer")
	req := httptest.NewRequest(http.MethodPost, "/NightlyCleanup", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
