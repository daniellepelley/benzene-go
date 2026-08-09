package httpbinding_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// ExampleHandler mounts a benzene pipeline as an ordinary http.Handler: a POST to /greet is routed
// to the "greet" topic's handler and its Result is mapped back to a real HTTP status and JSON body.
// The http.Handler composes with net/http like any other - httptest here, an http.Server in
// production.
func ExampleHandler() {
	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greetRequest, greetResponse](func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
			return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
		}))
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}

	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	server := httptest.NewServer(httpbinding.Handler(builder, routes))
	defer server.Close()

	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":"World"}`))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	fmt.Println(resp.StatusCode)
	fmt.Println(strings.TrimSpace(string(body)))
	// Output:
	// 200
	// {"greeting":"Hello, World!"}
}
