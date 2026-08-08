# benzene-go templates

Starter-project templates for Benzene services in Go — the `gonew` equivalent of .NET's
`dotnet new`. Each subdirectory is a **complete, buildable Go module**: a composition root, a demo
handler with one injected service, the transport host wiring, a component test that drives a real
message through the whole pipeline, and the deployment files for its target.

[`gonew`](https://pkg.go.dev/golang.org/x/tools/cmd/gonew) instantiates a new module by **copying
a template module and rewriting its module path** to the one you choose. So generating a project is
one command — no template engine, no placeholders to fill in beyond the module path.

## Templates

| Template | Trigger | `gonew` command |
|---|---|---|
| **`aws-apigateway`** | AWS Lambda, fronted by API Gateway HTTP requests (and direct wire-envelope invokes). | `go run golang.org/x/tools/cmd/gonew@latest github.com/daniellepelley/benzene-go/templates/aws-apigateway example.com/myservice` |
| **`aws-sqs`** | AWS Lambda, triggered by an SQS event source mapping. | `go run golang.org/x/tools/cmd/gonew@latest github.com/daniellepelley/benzene-go/templates/aws-sqs example.com/myservice` |

Replace `example.com/myservice` with your own module path (the last path segment becomes the new
directory name). For example:

```bash
go run golang.org/x/tools/cmd/gonew@latest \
  github.com/daniellepelley/benzene-go/templates/aws-apigateway \
  example.com/myservice
cd myservice
go test ./...
```

`gonew` copies the module, rewrites `module github.com/daniellepelley/benzene-go/templates/aws-apigateway`
to `module example.com/myservice` (and any internal imports with it), and leaves everything else —
`main.go`, `greeter.go`, `main_test.go`, `template.yaml`, `Dockerfile`, `README.md` — intact for
you to edit. The generated `README.md` uses `{{MODULE}}` as a stand-in for your module name.

## What every template contains

The same shape across transports, so you learn it once (mirroring the .NET template pack):

- **A composition root** — `newApp()` in `main.go`, the three-phase `benzene.App`
  (`GetConfiguration` → `ConfigureServices` → `Configure`) that both `main()` and the tests boot
  from, so the test exercises exactly the wiring that ships.
- **A demo handler with one injected service** — `greetHandler` depends on a `Greeter` port
  (`greeter.go`), resolved from the invocation's DI scope. It's the "hello world" of the
  hexagonal shape: swap the adapter without touching the handler.
- **The transport host** — the `main()` entry point wired to that transport's binding
  (`awslambda.Start(...)` with `awssqs.Handler` / the API-Gateway HTTP+envelope dispatch).
- **A component test** — `main_test.go` boots the real app via
  [`benzenetest`](../benzenetest) and pushes a native transport event through the whole pipeline,
  using a spy `Greeter` to assert the handler actually ran with the routed message. Only the
  transport trigger is simulated; the pipeline is real.
- **Deployment files** — an AWS SAM `template.yaml` and a `Dockerfile` (container-image Lambda on
  the `provided.al2023` runtime). Both are hand-checked against the documented AWS shapes, not
  deployed from the template — review before production use.

## Module resolution: `go.work`, not a shipped `replace`

Each template's `go.mod` requires `github.com/daniellepelley/benzene-go` (and, for `aws-sqs`, the
`awssqs` module) at their published version, with **no `replace` directive** — a shipped `replace`
pointing at `../..` would break the moment `gonew` copies the module somewhere outside this repo.

For local development inside this repo, the root [`go.work`](../go.work) lists both template
modules under `use` and carries the workspace-scoped `replace` lines that resolve the
not-yet-published `v0.1.0` to the local checkout. Those `replace`s live in `go.work` (never in a
template's `go.mod`), so they apply only here and never travel with a generated project.

A freshly `gonew`-generated project therefore needs the Benzene modules to be resolvable the
normal way — from the module proxy once they're tagged/published, or via a `replace` **you** add
pointing at a local checkout while the modules are unpublished, e.g.:

```bash
# in the generated project, while benzene-go is not yet published:
go mod edit -replace github.com/daniellepelley/benzene-go=/path/to/benzene-go
go mod tidy
```

## Verifying the templates (maintainers)

From the repo root, each template module builds and tests on its own under the workspace:

```bash
( cd templates/aws-apigateway && go build ./... && go test ./... )
( cd templates/aws-sqs        && go build ./... && go test ./... )
```

These modules have their own `go.mod`, so the root module's `go build ./...` / `go test ./...` do
**not** cross into them (a nested module boundary stops `./...`), and adding them to `go.work`
leaves the root build green.
