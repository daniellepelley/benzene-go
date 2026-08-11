// Package greeting is the request/response shape and handler shared by all three binaries in this
// example: api (HTTP), sqsworker (a self-hosted SQS consumer), and kafkaworker (a self-hosted Kafka
// consumer). Nothing in Handler knows which of the three called it - no transport type, no queue/
// topic name, no HTTP context. That's deliberate: it's what "write the handler once, run it behind
// whichever transport reaches a pod fastest for the traffic you actually have" means in practice,
// not just as a slogan. See docs/getting-started-kubernetes.md for the full walkthrough.
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
