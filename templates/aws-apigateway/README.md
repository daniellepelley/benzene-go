# {{MODULE}} — Benzene on AWS Lambda + API Gateway

A [Benzene](https://github.com/daniellepelley/Benzene) service on AWS Lambda, triggered by API
Gateway HTTP requests. Generated from the benzene-go `aws-apigateway` template.

## What's here

| File | Purpose |
|---|---|
| `main.go` | Composition root (`newApp`), the demo `greetHandler`, the HTTP route table, and the Lambda entry point that dispatches API-Gateway-HTTP vs. wire-envelope events. |
| `greeter.go` | The one injected dependency — a `Greeter` port and its default adapter, wired up in `newApp`. Your handler resolves it from the DI scope; swap the adapter without touching the handler. |
| `main_test.go` | A component test: boots the real app and pushes a native API Gateway event through the whole pipeline, asserting (via a spy `Greeter`) that the handler actually ran. |
| `template.yaml` | AWS SAM template — a container-image Lambda fronted by an API Gateway HTTP API. |
| `Dockerfile` | Builds the `bootstrap` binary for the `provided.al2023` custom runtime. |

## Build and test

```bash
go build ./...
go test ./...
```

## Deploy

Requires the [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured. `template.yaml` is hand-checked against the pattern
in the Benzene docs but **not** validated or deployed from this template — review it first.

```bash
sam build
sam deploy --guided
```

The output includes the API Gateway base URL:

```bash
curl -X POST "$API_URL/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "$API_URL/greet" -d '{"name":""}'
# 400 Bad Request
```

## Where to go next

- **`greetHandler` in `main.go`** is where your logic goes — replace it, or add more handlers,
  registering each on its own topic in `newApp`.
- **`greeter.go`** shows the injected-dependency pattern: depend on an interface, register a
  concrete adapter, resolve it in the handler via `benzene.ScopeFromContext` +
  `benzene.GetService`.
- A handler routes by **topic**, so the same handler runs unchanged on every Benzene transport.
  Want a queue trigger instead? Start from the `aws-sqs` template — same handler shape, a
  different front door.
- Full guide: the [benzene-go](https://github.com/daniellepelley/benzene-go) README and the
  cross-language [specification](https://github.com/daniellepelley/Benzene/tree/main/docs/specification).
