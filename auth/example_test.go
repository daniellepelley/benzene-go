package auth_test

import (
	"context"
	"encoding/base64"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/auth"
)

// ExampleBasicAuth shows HTTP Basic authentication as middleware. It validates the Authorization
// header with an app-supplied credential check and either sets the authenticated Principal on the
// context - so the handler (and any RequireRole/RequireScope downstream) can read it - or
// short-circuits with an unauthorized result before the handler ever runs. BearerAuth is the
// OAuth2/JWT sibling with the same shape.
func ExampleBasicAuth() {
	validate := func(user, pass string) (auth.Principal, bool) {
		if user == "alice" && pass == "secret" {
			return auth.Principal{Name: user, Roles: []string{"admin"}}, true
		}
		return auth.Principal{}, false
	}

	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("secure"),
		benzene.Handler[struct{}, string](func(ctx context.Context, _ struct{}) benzene.Result[string] {
			principal, _ := auth.PrincipalFromContext(ctx)
			return benzene.Ok("hello " + principal.Name)
		}))
	pipeline := benzene.NewPipeline(auth.BasicAuth(validate, "example"), benzene.RouterMiddleware(registry))

	run := func(header string) benzene.Status {
		ic := benzene.NewInvocationContext(benzene.NewTopic("secure"),
			map[string]string{"authorization": header}, struct{}{}, benzene.NewContainer().NewScope())
		if err := pipeline.Run(context.Background(), ic); err != nil {
			panic(err)
		}
		return ic.Result.ResultStatus()
	}

	fmt.Println("valid:  ", run("Basic "+base64.StdEncoding.EncodeToString([]byte("alice:secret"))))
	fmt.Println("invalid:", run("Basic "+base64.StdEncoding.EncodeToString([]byte("alice:wrong"))))
	// Output:
	// valid:   ok
	// invalid: unauthorized
}
