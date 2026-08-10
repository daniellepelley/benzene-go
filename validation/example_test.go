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
// a plain function returning the list of problems (empty means valid); the wrapped value is an
// ordinary benzene.Handler, so it registers and composes like any other.
func ExampleValidated() {
	validate := validation.ValidatorFunc[createUser](func(u createUser) []string {
		var problems []string
		if u.Name == "" {
			problems = append(problems, "name is required")
		}
		if u.Email == "" {
			problems = append(problems, "email is required")
		}
		return problems
	})

	handler := validation.Validated(validate, func(_ context.Context, u createUser) benzene.Result[string] {
		return benzene.Ok("created " + u.Name)
	})

	valid := handler(context.Background(), createUser{Name: "Ada", Email: "ada@example.com"})
	fmt.Println(valid.ResultStatus(), "-", valid.ResultPayload())

	invalid := handler(context.Background(), createUser{})
	fmt.Println(invalid.ResultStatus(), "-", invalid.ResultErrors())
	// Output:
	// ok - created Ada
	// validation-error - [name is required email is required]
}
