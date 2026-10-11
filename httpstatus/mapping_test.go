package httpstatus

import (
	"strconv"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// These tables are cross-checked against daniellepelley/Benzene's
// docs/specification/conformance/http-status-mapping.json - the authoritative fixture this
// package must agree with. The conformance runner (a later package in this module) proves
// this directly against that JSON file; these are this package's own focused unit tests.

func TestToHTTP_ForwardTable(t *testing.T) {
	tests := []struct {
		status benzene.Status
		want   int
	}{
		{benzene.StatusOk, 200},
		{benzene.StatusIgnored, 200},
		{benzene.StatusCreated, 201},
		{benzene.StatusAccepted, 202},
		{benzene.StatusUpdated, 204},
		{benzene.StatusDeleted, 204},
		{benzene.StatusBadRequest, 400},
		{benzene.StatusUnauthorized, 401},
		{benzene.StatusForbidden, 403},
		{benzene.StatusNotFound, 404},
		{benzene.StatusConflict, 409},
		{benzene.StatusValidationError, 422},
		{benzene.StatusTooManyRequests, 429},
		{benzene.StatusUnexpectedError, 500},
		{benzene.StatusNotImplemented, 501},
		{benzene.StatusServiceUnavailable, 503},
		{benzene.StatusTimeout, 504},
		{benzene.Status("<unknown>"), 500},
		{benzene.Status(""), 500},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := ToHTTP(tt.status); got != tt.want {
				t.Errorf("ToHTTP(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

func TestFromHTTP_ReverseTable(t *testing.T) {
	tests := []struct {
		code int
		want benzene.Status
	}{
		{200, benzene.StatusOk},
		{201, benzene.StatusCreated},
		{202, benzene.StatusAccepted},
		{204, benzene.StatusDeleted},
		{400, benzene.StatusBadRequest},
		{401, benzene.StatusUnauthorized},
		{403, benzene.StatusForbidden},
		{404, benzene.StatusNotFound},
		{408, benzene.StatusTimeout},
		{409, benzene.StatusConflict},
		{422, benzene.StatusValidationError},
		{429, benzene.StatusTooManyRequests},
		{500, benzene.StatusUnexpectedError},
		{501, benzene.StatusNotImplemented},
		{502, benzene.StatusServiceUnavailable},
		{503, benzene.StatusServiceUnavailable},
		{504, benzene.StatusTimeout},
		{418, benzene.StatusUnexpectedError}, // "anything else" fixture case
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			if got := FromHTTP(tt.code); got != tt.want {
				t.Errorf("FromHTTP(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestResponse_HealthRouteAnswersFromTheReport(t *testing.T) {
	ok := true
	report := func(healthy bool) wire.Response {
		body := `{"isHealthy":true,"healthChecks":{"db":{"status":"warning"}}}`
		if !healthy {
			body = `{"isHealthy":false,"healthChecks":{"db":{"status":"failed"}}}`
		}
		return wire.Response{StatusCode: "ok", Headers: map[string]string{"content-type": "application/json"}, Body: body, IsSuccessful: &ok}
	}

	tests := []struct {
		name  string
		topic string
		resp  wire.Response
		want  int
	}{
		{"unhealthy healthcheck is 503", "benzene:healthcheck", report(false), 503},
		{"healthy (a warning included) is 200", "benzene:healthcheck", report(true), 200},
		{"unhealthy liveness is 503", "benzene:liveness", report(false), 503},
		{"unhealthy readiness is 503", "benzene:readiness", report(false), 503},
		{"an ordinary topic answers from its status", "orders:get", report(false), 200},
		{"a health body that is not a report answers from its status", "benzene:healthcheck", wire.Response{StatusCode: "ok", Body: `"up"`, IsSuccessful: &ok}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body, headers := Response(benzene.NewTopic(tt.topic), tt.resp)
			if code != tt.want {
				t.Errorf("code = %d, want %d", code, tt.want)
			}
			if body != tt.resp.Body {
				t.Errorf("body = %s, want the report unchanged (no problem document, no status member)", body)
			}
			if got := headers["content-type"]; got != tt.resp.Headers["content-type"] {
				t.Errorf("content-type = %q, want %q unchanged (a health report is not a problem document)", got, tt.resp.Headers["content-type"])
			}
		})
	}
}

func TestResponse_FailedHealthInvocationMapsAsUsual(t *testing.T) {
	failed := false
	code, body, headers := Response(benzene.NewTopic("benzene:healthcheck"), wire.Response{
		StatusCode:   "unexpected-error",
		Headers:      map[string]string{"content-type": "application/problem+json"},
		Body:         `{"benzeneStatus":"unexpected-error"}`,
		IsSuccessful: &failed,
	})
	if code != 500 || headers["content-type"] != "application/problem+json" || !strings.Contains(body, `"status":500`) {
		t.Errorf("Response() = (%d, %s, %v), want the §4.1 problem rendering for a failed health invocation", code, body, headers)
	}
}
