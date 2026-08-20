package validation

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type createOrder struct {
	ID     string
	Amount float64
}

// orderMessages is an ordinary Go validation function that only has messages - the shape a service
// writes when there is nothing to add beyond the sentence. Messages() adapts it.
func orderMessages(o createOrder) []string {
	var errs []string
	if o.ID == "" {
		errs = append(errs, "id is required")
	}
	if o.Amount <= 0 {
		errs = append(errs, "amount must be positive")
	}
	return errs
}

// orderValidator is the same rules stated structurally: each error names the field it came from and
// the rule that rejected it, which is what reaches the caller's problem document.
func orderValidator(o createOrder) []benzene.Error {
	var errs []benzene.Error
	if o.ID == "" {
		errs = append(errs, benzene.Error{Message: "id is required", Field: "ID", Code: "required"})
	}
	if o.Amount <= 0 {
		errs = append(errs, benzene.Error{Message: "amount must be positive", Field: "Amount", Code: "positive"})
	}
	return errs
}

func TestValidated_ValidRequestRunsHandler(t *testing.T) {
	ran := false
	handler := Validated(ValidatorFunc[createOrder](orderValidator),
		func(context.Context, createOrder) benzene.Result[string] {
			ran = true
			return benzene.Ok("created")
		})

	result := handler(context.Background(), createOrder{ID: "o-1", Amount: 9.99})

	if !ran {
		t.Error("handler did not run for a valid request")
	}
	if result.Status != benzene.StatusOk || *result.Payload != "created" {
		t.Errorf("result = %+v, want Ok(\"created\")", result)
	}
}

func TestValidated_InvalidRequestShortCircuits(t *testing.T) {
	ran := false
	handler := Validated(ValidatorFunc[createOrder](orderValidator),
		func(context.Context, createOrder) benzene.Result[string] {
			ran = true
			return benzene.Ok("created")
		})

	result := handler(context.Background(), createOrder{ID: "", Amount: 0})

	if ran {
		t.Error("handler ran despite an invalid request - it must be short-circuited")
	}
	if result.Status != benzene.StatusValidationError {
		t.Errorf("status = %q, want %q", result.Status, benzene.StatusValidationError)
	}
	if len(result.Errors) != 2 || result.Errors[0].Message != "id is required" || result.Errors[1].Message != "amount must be positive" {
		t.Errorf("errors = %v, want both validation messages", result.Errors)
	}
	if result.IsSuccessful() {
		t.Error("a validation-error result must not be successful")
	}
}

func TestValidated_NilValidatorReturnsHandlerUnchanged(t *testing.T) {
	// A nil validator wraps nothing: the handler runs and its result is returned as-is (no panic).
	ran := false
	handler := Validated[createOrder, string](nil, func(context.Context, createOrder) benzene.Result[string] {
		ran = true
		return benzene.Ok("created")
	})
	result := handler(context.Background(), createOrder{}) // an "invalid" order, but no validator
	if !ran {
		t.Error("handler did not run with a nil validator")
	}
	if result.Status != benzene.StatusOk {
		t.Errorf("status = %q, want ok (nil validator applies no validation)", result.Status)
	}
}

func TestValidatorFunc_Validate(t *testing.T) {
	v := ValidatorFunc[createOrder](orderValidator)
	if got := v.Validate(createOrder{ID: "x", Amount: 1}); got != nil {
		t.Errorf("Validate(valid) = %v, want nil", got)
	}
	if got := v.Validate(createOrder{}); len(got) != 2 {
		t.Errorf("Validate(invalid) = %v, want 2 errors", got)
	}
}

func TestValidated_StructuredErrorsReachTheResult(t *testing.T) {
	handler := Validated(ValidatorFunc[createOrder](orderValidator),
		func(context.Context, createOrder) benzene.Result[string] { return benzene.Ok("created") })

	result := handler(context.Background(), createOrder{})

	if len(result.Errors) != 2 {
		t.Fatalf("len(Errors) = %d, want 2", len(result.Errors))
	}
	if result.Errors[0].Field != "ID" || result.Errors[0].Code != "required" {
		t.Errorf("Errors[0] = %+v, want the field and code the validator stated", result.Errors[0])
	}
	if result.Errors[1].Field != "Amount" || result.Errors[1].Code != "positive" {
		t.Errorf("Errors[1] = %+v, want the field and code the validator stated", result.Errors[1])
	}
}

func TestMessages_WrapsPlainMessagesAsMessageOnlyErrors(t *testing.T) {
	v := Messages(orderMessages)

	if got := v.Validate(createOrder{ID: "x", Amount: 1}); got != nil {
		t.Errorf("Validate(valid) = %v, want nil", got)
	}

	got := v.Validate(createOrder{})
	if len(got) != 2 {
		t.Fatalf("Validate(invalid) = %v, want 2 errors", got)
	}
	if got[0].Message != "id is required" || got[0].Field != "" || got[0].Code != "" {
		t.Errorf("got[0] = %+v, want the message alone - Messages must not invent a field or code", got[0])
	}
}

func TestMessages_NilFunctionIsNilValidator(t *testing.T) {
	// So Validated's nil-tolerance still applies to a validator built from a nil function, rather
	// than producing a non-nil Validator that panics on the first request.
	if v := Messages[createOrder](nil); v != nil {
		t.Errorf("Messages(nil) = %v, want nil", v)
	}
}

func TestCombine_ConcatenatesAndSkipsNil(t *testing.T) {
	idRequired := Messages(func(o createOrder) []string {
		if o.ID == "" {
			return []string{"id is required"}
		}
		return nil
	})
	amountPositive := Messages(func(o createOrder) []string {
		if o.Amount <= 0 {
			return []string{"amount must be positive"}
		}
		return nil
	})

	combined := Combine[createOrder](idRequired, nil, amountPositive)

	if got := combined.Validate(createOrder{ID: "o-1", Amount: 5}); got != nil {
		t.Errorf("Validate(valid) = %v, want nil", got)
	}
	got := combined.Validate(createOrder{})
	if len(got) != 2 || got[0].Message != "id is required" || got[1].Message != "amount must be positive" {
		t.Errorf("Validate(invalid) = %v, want both messages in order", got)
	}
}

func TestCombine_NoValidatorsAlwaysPasses(t *testing.T) {
	if got := Combine[createOrder]().Validate(createOrder{}); got != nil {
		t.Errorf("empty Combine = %v, want nil (always passes)", got)
	}
}

// Validated composes end to end through the pipeline: a registered validated handler short-circuits
// an invalid request to validation-error without the handler running.
func TestValidated_ComposesThroughRegistry(t *testing.T) {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("order:create"),
		Validated(ValidatorFunc[createOrder](orderValidator),
			func(context.Context, createOrder) benzene.Result[string] { return benzene.Ok("created") })); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !registry.Has(benzene.NewTopic("order:create")) {
		t.Fatal("validated handler did not register as an ordinary handler")
	}
}
