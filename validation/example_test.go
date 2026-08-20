package validation_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/validation"
)

type createUser struct {
	Name  string
	Email string
}

// ExampleValidated wraps a handler so an invalid request short-circuits to a validation-error result
// before the handler runs - the handler itself stays focused on the happy path. ValidatorFunc adapts
// a plain function returning the problems (empty means valid); the wrapped value is an ordinary
// benzene.Handler, so it registers and composes like any other.
//
// Each problem names the field it came from and the rule that rejected it, and both reach the
// caller's problem document rather than being flattened into prose it would have to parse.
func ExampleValidated() {
	validate := validation.ValidatorFunc[createUser](func(u createUser) []benzene.Error {
		var problems []benzene.Error
		if u.Name == "" {
			problems = append(problems, benzene.Error{Message: "name is required", Field: "Name", Code: "required"})
		}
		if u.Email == "" {
			problems = append(problems, benzene.Error{Message: "email is required", Field: "Email", Code: "required"})
		}
		return problems
	})

	handler := validation.Validated(validate, func(_ context.Context, u createUser) benzene.Result[string] {
		return benzene.Ok("created " + u.Name)
	})

	valid := handler(context.Background(), createUser{Name: "Ada", Email: "ada@example.com"})
	fmt.Println(valid.ResultStatus(), "-", valid.ResultPayload())

	invalid := handler(context.Background(), createUser{})
	fmt.Println(invalid.ResultStatus())
	for _, problem := range invalid.Errors {
		fmt.Printf("  %s (field=%s code=%s)\n", problem.Message, problem.Field, problem.Code)
	}
	// Output:
	// ok - created Ada
	// validation-error
	//   name is required (field=Name code=required)
	//   email is required (field=Email code=required)
}

// ExampleMessages is the short path for a validator that only has messages: Messages wraps each
// string as a Message-only error, so a service with nothing to add beyond the sentence writes an
// ordinary []string function and nothing more.
func ExampleMessages() {
	validate := validation.Messages(func(u createUser) []string {
		var problems []string
		if u.Name == "" {
			problems = append(problems, "name is required")
		}
		return problems
	})

	handler := validation.Validated(validate, func(_ context.Context, u createUser) benzene.Result[string] {
		return benzene.Ok("created " + u.Name)
	})

	invalid := handler(context.Background(), createUser{})
	fmt.Println(invalid.ResultStatus(), "-", invalid.ResultErrors())
	// Output:
	// validation-error - [name is required]
}
