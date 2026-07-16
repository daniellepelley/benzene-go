package awslambda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRuntimeServer simulates just enough of the Lambda Runtime API
// (https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html) for runOnce's one GET-next
// + one POST-response-or-error cycle to be tested without a real Lambda execution environment.
type fakeRuntimeServer struct {
	*httptest.Server

	mu             sync.Mutex
	nextStatus     int
	nextBody       string
	nextHeaders    map[string]string
	responseStatus int
	errorStatus    int

	postedResponseBody string
	postedErrorBody    string
	postedErrorPath    string
}

func newFakeRuntimeServer() *fakeRuntimeServer {
	f := &fakeRuntimeServer{nextStatus: http.StatusOK, responseStatus: http.StatusAccepted, errorStatus: http.StatusAccepted}
	mux := http.NewServeMux()
	mux.HandleFunc("/2018-06-01/runtime/invocation/next", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for k, v := range f.nextHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(f.nextStatus)
		w.Write([]byte(f.nextBody))
	})
	mux.HandleFunc("/2018-06-01/runtime/invocation/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body := readBody(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/response"):
			f.postedResponseBody = body
			w.WriteHeader(f.responseStatus)
		case strings.HasSuffix(r.URL.Path, "/error"):
			f.postedErrorBody = body
			f.postedErrorPath = r.URL.Path
			w.WriteHeader(f.errorStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	f.Server = httptest.NewServer(mux)
	return f
}

func readBody(r *http.Request) string {
	data, _ := io.ReadAll(r.Body)
	return string(data)
}

func (f *fakeRuntimeServer) client() *runtimeClient {
	return &runtimeClient{baseURL: f.URL + "/2018-06-01/runtime", httpClient: http.DefaultClient}
}

func TestRunOnce_SuccessfulInvocationPostsResponse(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{"hello":"world"}`
	server.nextHeaders = map[string]string{
		"Lambda-Runtime-Aws-Request-Id": "req-1",
		"Lambda-Runtime-Deadline-Ms":    strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10),
		"Lambda-Runtime-Trace-Id":       "trace-abc",
	}

	var gotEvent string
	handler := HandlerFunc(func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		gotEvent = string(event)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("ctx should carry a deadline derived from Lambda-Runtime-Deadline-Ms")
		} else if deadline.Before(time.Now()) {
			t.Error("ctx deadline should be in the future")
		}
		return json.RawMessage(`{"ok":true}`), nil
	})

	if err := runOnce(context.Background(), server.client(), handler); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if gotEvent != `{"hello":"world"}` {
		t.Errorf("handler received %q, want %q", gotEvent, `{"hello":"world"}`)
	}
	if server.postedResponseBody != `{"ok":true}` {
		t.Errorf("posted response body = %q, want %q", server.postedResponseBody, `{"ok":true}`)
	}
}

func TestRunOnce_NoDeadlineHeaderMeansNoContextDeadline(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{}`
	server.nextHeaders = map[string]string{"Lambda-Runtime-Aws-Request-Id": "req-1"}

	handler := HandlerFunc(func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("ctx should not carry a deadline when Lambda-Runtime-Deadline-Ms is absent")
		}
		return json.RawMessage(`{}`), nil
	})

	if err := runOnce(context.Background(), server.client(), handler); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
}

func TestRunOnce_HandlerErrorPostsToErrorEndpoint(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{}`
	server.nextHeaders = map[string]string{"Lambda-Runtime-Aws-Request-Id": "req-2"}

	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("boom")
	})

	if err := runOnce(context.Background(), server.client(), handler); err != nil {
		t.Fatalf("runOnce() error = %v, want nil - a handler error is reported, not a runtime failure", err)
	}
	if !strings.Contains(server.postedErrorPath, "req-2") {
		t.Errorf("posted error path = %q, want it to reference req-2", server.postedErrorPath)
	}
	var runtimeErr runtimeError
	if err := json.Unmarshal([]byte(server.postedErrorBody), &runtimeErr); err != nil {
		t.Fatalf("posted error body is not valid JSON: %v; body = %s", err, server.postedErrorBody)
	}
	if !strings.Contains(runtimeErr.ErrorMessage, "boom") {
		t.Errorf("ErrorMessage = %q, want it to mention %q", runtimeErr.ErrorMessage, "boom")
	}
}

func TestRunOnce_HandlerPanicPostsToErrorEndpoint(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{}`
	server.nextHeaders = map[string]string{"Lambda-Runtime-Aws-Request-Id": "req-3"}

	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("kaboom")
	})

	if err := runOnce(context.Background(), server.client(), handler); err != nil {
		t.Fatalf("runOnce() error = %v, want nil - a handler panic must not crash the bootstrap loop", err)
	}
	if !strings.Contains(server.postedErrorBody, "kaboom") {
		t.Errorf("posted error body = %q, want it to mention the panic value", server.postedErrorBody)
	}
}

func TestRunOnce_NextInvocationTransportFailureIsReturned(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextStatus = http.StatusInternalServerError

	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("handler should not be called when fetching the next invocation fails")
		return nil, nil
	})

	if err := runOnce(context.Background(), server.client(), handler); err == nil {
		t.Error("runOnce() error = nil, want an error when the runtime API returns a bad status")
	}
}

func TestRunOnce_PostResponseTransportFailureIsReturned(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{}`
	server.nextHeaders = map[string]string{"Lambda-Runtime-Aws-Request-Id": "req-4"}
	server.responseStatus = http.StatusInternalServerError

	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})

	if err := runOnce(context.Background(), server.client(), handler); err == nil {
		t.Error("runOnce() error = nil, want an error when posting the response fails")
	}
}

func TestRunOnce_PostErrorTransportFailureIsReturned(t *testing.T) {
	server := newFakeRuntimeServer()
	defer server.Close()
	server.nextBody = `{}`
	server.nextHeaders = map[string]string{"Lambda-Runtime-Aws-Request-Id": "req-5"}
	server.errorStatus = http.StatusInternalServerError

	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("boom")
	})

	if err := runOnce(context.Background(), server.client(), handler); err == nil {
		t.Error("runOnce() error = nil, want an error when posting the handler error fails")
	}
}

func TestRunOnce_NextInvocationNetworkErrorIsReturned(t *testing.T) {
	client := &runtimeClient{baseURL: "http://127.0.0.1:1/2018-06-01/runtime", httpClient: &http.Client{Timeout: time.Second}}
	handler := HandlerFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	})

	if err := runOnce(context.Background(), client, handler); err == nil {
		t.Error("runOnce() error = nil, want a network error connecting to an unreachable runtime API")
	}
}

func TestNewRuntimeClientFromEnv_MissingEnvVarIsError(t *testing.T) {
	t.Setenv("AWS_LAMBDA_RUNTIME_API", "")
	if _, err := newRuntimeClientFromEnv(); err == nil {
		t.Error("newRuntimeClientFromEnv() error = nil, want an error when AWS_LAMBDA_RUNTIME_API is unset")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestNext_ResponseBodyReadErrorIsReturned(t *testing.T) {
	client := &runtimeClient{baseURL: "http://example.invalid/2018-06-01/runtime", httpClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}, Header: make(http.Header)}, nil
		}),
	}}

	if _, err := client.next(); err == nil {
		t.Error("next() error = nil, want an error when the response body fails to read")
	}
}

func TestPostResponse_TransportErrorIsReturned(t *testing.T) {
	client := &runtimeClient{baseURL: "http://example.invalid/2018-06-01/runtime", httpClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}}

	if err := client.postResponse("req-1", json.RawMessage(`{}`)); err == nil {
		t.Error("postResponse() error = nil, want an error when the transport fails")
	}
}

func TestPostError_TransportErrorIsReturned(t *testing.T) {
	client := &runtimeClient{baseURL: "http://example.invalid/2018-06-01/runtime", httpClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}}

	if err := client.postError("req-1", errors.New("boom")); err == nil {
		t.Error("postError() error = nil, want an error when the transport fails")
	}
}

func TestPostError_InvalidRequestIDIsRequestBuildError(t *testing.T) {
	client := &runtimeClient{baseURL: "http://example.invalid/2018-06-01/runtime", httpClient: http.DefaultClient}

	// A control character makes the resulting URL fail to parse in http.NewRequest, before
	// any network call is attempted.
	if err := client.postError("abc\ndef", errors.New("boom")); err == nil {
		t.Error("postError() error = nil, want an error when the request can't be built")
	}
}

func TestNewRuntimeClientFromEnv_UsesEnvVar(t *testing.T) {
	t.Setenv("AWS_LAMBDA_RUNTIME_API", "127.0.0.1:9001")
	client, err := newRuntimeClientFromEnv()
	if err != nil {
		t.Fatalf("newRuntimeClientFromEnv() error = %v", err)
	}
	if client.baseURL != "http://127.0.0.1:9001/2018-06-01/runtime" {
		t.Errorf("baseURL = %q, want %q", client.baseURL, "http://127.0.0.1:9001/2018-06-01/runtime")
	}
}
