package meshd

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestViewHandler(t *testing.T) {
	tests := []struct {
		name         string
		envelopePath string
		wantPath     string
	}{
		{name: "default envelope path", envelopePath: "", wantPath: "/benzene/invoke"},
		{name: "custom envelope path", envelopePath: "/mesh/envelope", wantPath: "/mesh/envelope"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestCollector(t).ViewHandler(tt.envelopePath)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))

			if recorder.Code != 200 {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if got := recorder.Header().Get("content-type"); !strings.HasPrefix(got, "text/html") {
				t.Errorf("content-type = %q, want text/html", got)
			}
			body, _ := io.ReadAll(recorder.Body)
			page := string(body)
			if !strings.Contains(page, "Benzene Mesh") {
				t.Error("page does not contain the title")
			}
			if !strings.Contains(page, `"`+tt.wantPath+`"`) {
				t.Errorf("page does not reference the envelope path %q", tt.wantPath)
			}
			if strings.Contains(page, "__ENVELOPE_PATH__") {
				t.Error("placeholder was not replaced")
			}
			if !strings.Contains(page, "mesh:query:fleet") {
				t.Error("page does not query the fleet topic")
			}
		})
	}
}
