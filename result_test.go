package benzene

import (
	"reflect"
	"testing"
)

type greeting struct {
	Message string
}

func TestOk(t *testing.T) {
	r := Ok(greeting{Message: "hi"})

	if r.Status != StatusOk {
		t.Errorf("Status = %q, want %q", r.Status, StatusOk)
	}
	if !r.IsSuccessful() {
		t.Error("IsSuccessful() = false, want true")
	}
	if r.Payload == nil || r.Payload.Message != "hi" {
		t.Errorf("Payload = %+v, want {Message: hi}", r.Payload)
	}
	if len(r.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", r.Errors)
	}
}

func TestSuccessConstructors(t *testing.T) {
	tests := []struct {
		name       string
		result     Result[greeting]
		wantStatus Status
	}{
		{"Ok", Ok(greeting{}), StatusOk},
		{"CreatedResult", CreatedResult(greeting{}), StatusCreated},
		{"Accepted", Accepted(greeting{}), StatusAccepted},
		{"Updated", Updated(greeting{}), StatusUpdated},
		{"Deleted", Deleted(greeting{}), StatusDeleted},
		{"Ignored", Ignored(greeting{}), StatusIgnored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", tt.result.Status, tt.wantStatus)
			}
			if !tt.result.IsSuccessful() {
				t.Errorf("%s result should be successful", tt.name)
			}
		})
	}
}

func TestFailureConstructors(t *testing.T) {
	tests := []struct {
		name       string
		result     Result[greeting]
		wantStatus Status
	}{
		{"BadRequest", BadRequest[greeting]("bad"), StatusBadRequest},
		{"ValidationError", ValidationError[greeting]("invalid"), StatusValidationError},
		{"Unauthorized", Unauthorized[greeting](), StatusUnauthorized},
		{"Forbidden", Forbidden[greeting](), StatusForbidden},
		{"NotFound", NotFound[greeting]("missing"), StatusNotFound},
		{"Conflict", Conflict[greeting](), StatusConflict},
		{"NotImplemented", NotImplemented[greeting](), StatusNotImplemented},
		{"ServiceUnavailable", ServiceUnavailable[greeting]("down"), StatusServiceUnavailable},
		{"UnexpectedError", UnexpectedError[greeting]("boom"), StatusUnexpectedError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", tt.result.Status, tt.wantStatus)
			}
			if tt.result.IsSuccessful() {
				t.Errorf("%s result should not be successful", tt.name)
			}
			if tt.result.Payload != nil {
				t.Errorf("Payload = %+v, want nil for a failure", tt.result.Payload)
			}
		})
	}
}

func TestFail_ErrorsPreserved(t *testing.T) {
	r := Fail[greeting](StatusNotFound, "no handler", "for topic order:create")
	want := []string{"no handler", "for topic order:create"}
	if !reflect.DeepEqual(r.Errors, want) {
		t.Errorf("Errors = %v, want %v", r.Errors, want)
	}
}

func TestFail_PanicsOnSuccessStatus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Fail with a success-class status should panic")
		}
	}()
	Fail[greeting](StatusOk)
}

func TestResultInfo_ExposesTypeErasedView(t *testing.T) {
	var info ResultInfo = Ok(greeting{Message: "hi"})

	if info.ResultStatus() != StatusOk {
		t.Errorf("ResultStatus() = %q, want %q", info.ResultStatus(), StatusOk)
	}
	payload, ok := info.ResultPayload().(greeting)
	if !ok || payload.Message != "hi" {
		t.Errorf("ResultPayload() = %v, want greeting{Message: hi}", info.ResultPayload())
	}
	if len(info.ResultErrors()) != 0 {
		t.Errorf("ResultErrors() = %v, want empty", info.ResultErrors())
	}
}

func TestResultInfo_NilPayloadOnFailure(t *testing.T) {
	var info ResultInfo = NotFound[greeting]("missing")
	if info.ResultPayload() != nil {
		t.Errorf("ResultPayload() = %v, want nil", info.ResultPayload())
	}
}
