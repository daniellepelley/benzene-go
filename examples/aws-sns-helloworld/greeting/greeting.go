// Package greeting is the request/response shape and handler shared by both Lambda functions
// in this example: publisher (HTTP-triggered, forwards to SNS) and consumer (SNS-triggered,
// actually runs Handler).
package greeting

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

type GreetRequest struct {
	Name string `json:"name"`
}

type GreetResponse struct {
	Greeting string `json:"greeting"`
}

func Handler(_ context.Context, req GreetRequest) benzene.Result[GreetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[GreetResponse]("name is required")
	}
	return benzene.Ok(GreetResponse{Greeting: "Hello, " + req.Name + "!"})
}
