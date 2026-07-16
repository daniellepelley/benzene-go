package cloudevents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/wire"
)

func TestParseStructured(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    Event
		wantErr string
	}{
		{
			name: "full event with extensions",
			body: `{"specversion":"1.0","id":"1","source":"/s","type":"greet",
				"datacontenttype":"application/json","dataschema":"schema","subject":"sub","time":"2026-01-01T00:00:00Z",
				"data":{"name":"World"},
				"traceparent":"00-abc-def-01","retrycount":3,"urgent":true,
				"nested":{"skipped":true},"listed":["skipped"],"nulled":null}`,
			want: Event{
				SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
				DataContentType: "application/json", DataSchema: "schema", Subject: "sub", Time: "2026-01-01T00:00:00Z",
				Data:       json.RawMessage(`{"name":"World"}`),
				Extensions: map[string]string{"traceparent": "00-abc-def-01", "retrycount": "3", "urgent": "true"},
			},
		},
		{
			name: "data_base64 decodes to a JSON string",
			body: `{"specversion":"1.0","id":"1","source":"/s","type":"greet","data_base64":"aGVsbG8="}`,
			want: Event{
				SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
				Data: json.RawMessage(`"hello"`), Extensions: map[string]string{},
			},
		},
		{name: "malformed JSON", body: `{not valid`, wantErr: "malformed structured event"},
		{name: "missing specversion", body: `{"id":"1","source":"/s","type":"greet"}`, wantErr: "unsupported specversion"},
		{name: "unsupported specversion", body: `{"specversion":"0.3","id":"1","source":"/s","type":"greet"}`, wantErr: "unsupported specversion"},
		{name: "non-string specversion", body: `{"specversion":1,"id":"1","source":"/s","type":"greet"}`, wantErr: "unsupported specversion"},
		{name: "missing type", body: `{"specversion":"1.0","id":"1","source":"/s"}`, wantErr: "missing required attribute"},
		{name: "missing id", body: `{"specversion":"1.0","source":"/s","type":"greet"}`, wantErr: "missing required attribute"},
		{name: "missing source", body: `{"specversion":"1.0","id":"1","type":"greet"}`, wantErr: "missing required attribute"},
		{name: "data_base64 not a string", body: `{"specversion":"1.0","id":"1","source":"/s","type":"greet","data_base64":3}`, wantErr: "data_base64 is not a JSON string"},
		{name: "data_base64 not base64", body: `{"specversion":"1.0","id":"1","source":"/s","type":"greet","data_base64":"!!"}`, wantErr: "not valid base64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStructured([]byte(tt.body))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseStructured() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStructured() error = %v", err)
			}
			assertEventsEqual(t, got, tt.want)
		})
	}
}

func assertEventsEqual(t *testing.T, got, want Event) {
	t.Helper()
	if got.SpecVersion != want.SpecVersion || got.ID != want.ID || got.Source != want.Source ||
		got.Type != want.Type || got.DataContentType != want.DataContentType ||
		got.DataSchema != want.DataSchema || got.Subject != want.Subject || got.Time != want.Time {
		t.Errorf("context attributes = %+v, want %+v", got, want)
	}
	if string(got.Data) != string(want.Data) {
		t.Errorf("Data = %s, want %s", got.Data, want.Data)
	}
	if len(got.Extensions) != len(want.Extensions) {
		t.Fatalf("Extensions = %v, want %v", got.Extensions, want.Extensions)
	}
	for k, v := range want.Extensions {
		if got.Extensions[k] != v {
			t.Errorf("Extensions[%q] = %q, want %q", k, got.Extensions[k], v)
		}
	}
}

func TestToRequest(t *testing.T) {
	tests := []struct {
		name        string
		event       Event
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "attributes and extensions become ce- headers",
			event: Event{
				SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
				DataContentType: "application/json", Subject: "sub", Time: "t",
				Data:       json.RawMessage(`{"name":"World"}`),
				Extensions: map[string]string{"traceparent": "00-abc-def-01"},
			},
			wantTopic: "greet",
			wantBody:  `{"name":"World"}`,
			wantHeaders: map[string]string{
				"ce-specversion": "1.0", "ce-id": "1", "ce-source": "/s",
				"ce-datacontenttype": "application/json", "ce-subject": "sub", "ce-time": "t",
				"ce-traceparent": "00-abc-def-01",
			},
		},
		{
			name: "text payload is unquoted",
			event: Event{
				SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
				DataContentType: "text/plain", Data: json.RawMessage(`"hello"`),
			},
			wantTopic: "greet",
			wantBody:  "hello",
			wantHeaders: map[string]string{
				"ce-specversion": "1.0", "ce-id": "1", "ce-source": "/s",
				"ce-datacontenttype": "text/plain",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ToRequest(tt.event)
			if req.Topic != tt.wantTopic {
				t.Errorf("Topic = %q, want %q", req.Topic, tt.wantTopic)
			}
			if req.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tt.wantBody)
			}
			if len(req.Headers) != len(tt.wantHeaders) {
				t.Fatalf("Headers = %v, want %v", req.Headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if req.Headers[k] != v {
					t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
				}
			}
		})
	}
}

func TestFromRequest(t *testing.T) {
	req := wire.Request{
		Topic: "greet",
		Headers: map[string]string{
			"ce-subject":         "sub",
			"ce-time":            "t",
			"ce-dataschema":      "schema",
			"ce-traceparent":     "00-abc-def-01",
			"CE-UPPERCASED":      "kept-after-lowercasing",
			"ce-id":              "must-not-leak",
			"ce-source":          "must-not-leak",
			"ce-specversion":     "0.3",
			"ce-not_legal!":      "dropped",
			"x-correlation-id":   "dropped",
			"plain-header":       "dropped",
			"ce-datacontenttype": "application/json",
		},
		Body: `{"name":"World"}`,
	}

	event := FromRequest(req, "id-1", "/go/service")

	if event.SpecVersion != "1.0" || event.ID != "id-1" || event.Source != "/go/service" || event.Type != "greet" {
		t.Errorf("identity attributes wrong: %+v", event)
	}
	if event.Subject != "sub" || event.Time != "t" || event.DataSchema != "schema" || event.DataContentType != "application/json" {
		t.Errorf("optional attributes wrong: %+v", event)
	}
	if string(event.Data) != `{"name":"World"}` {
		t.Errorf("Data = %s, want the body verbatim", event.Data)
	}
	wantExtensions := map[string]string{"traceparent": "00-abc-def-01", "uppercased": "kept-after-lowercasing"}
	if len(event.Extensions) != len(wantExtensions) {
		t.Fatalf("Extensions = %v, want %v", event.Extensions, wantExtensions)
	}
	for k, v := range wantExtensions {
		if event.Extensions[k] != v {
			t.Errorf("Extensions[%q] = %q, want %q", k, event.Extensions[k], v)
		}
	}
}

func TestFromRequest_NonJSONBodyBecomesTextData(t *testing.T) {
	event := FromRequest(wire.Request{Topic: "greet", Body: "plain text"}, "1", "/s")
	if string(event.Data) != `"plain text"` {
		t.Errorf("Data = %s, want a JSON string", event.Data)
	}
	if event.DataContentType != "text/plain" {
		t.Errorf("DataContentType = %q, want text/plain", event.DataContentType)
	}
}

func TestFromRequest_EmptyBodyHasNoData(t *testing.T) {
	event := FromRequest(wire.Request{Topic: "greet"}, "1", "/s")
	if event.Data != nil {
		t.Errorf("Data = %s, want nil", event.Data)
	}
}

func TestMarshalJSON_RoundTripsThroughParseStructured(t *testing.T) {
	original := Event{
		SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
		DataContentType: "application/json", DataSchema: "schema", Subject: "sub", Time: "t",
		Data:       json.RawMessage(`{"name":"World"}`),
		Extensions: map[string]string{"traceparent": "00-abc-def-01", "not legal": "dropped on marshal"},
	}
	body, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	parsed, err := ParseStructured(body)
	if err != nil {
		t.Fatalf("ParseStructured() error = %v", err)
	}
	want := original
	want.Extensions = map[string]string{"traceparent": "00-abc-def-01"}
	assertEventsEqual(t, parsed, want)
}

func TestMarshalJSON_NonJSONDataIsQuoted(t *testing.T) {
	event := Event{SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet", Data: json.RawMessage("raw text")}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		t.Fatalf("marshaled event is not valid JSON: %v; body = %s", err, body)
	}
	if string(members["data"]) != `"raw text"` {
		t.Errorf("data member = %s, want a JSON string", members["data"])
	}
}

func TestLegalExtensionName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"traceparent", true},
		{"abc123", true},
		{"", false},
		{"has-dash", false},
		{"UPPER", false},
		{"data", false},
		{"specversion", false},
		{"thisnameisfartoolongtouse", false},
	}
	for _, tt := range tests {
		if got := legalExtensionName(tt.name); got != tt.want {
			t.Errorf("legalExtensionName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
