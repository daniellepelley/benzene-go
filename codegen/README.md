# codegen

`benzene-codegen` (`cmd/benzene-codegen`): generates a typed, topic-scoped Go client SDK from a
Benzene service's committed Contract Document (`{Service}.spec.json`) - the Go port of the .NET
repo's `Benzene.CodeGen.Client`. See [../docs/codegen-client.md](../docs/codegen-client.md) for the
full guide (CLI usage, the two output shapes, naming, type mapping, the `contractHash` algorithm,
and the `go:generate` pattern), and [../examples/codegen-helloworld](../examples/codegen-helloworld)
for a full worked, dogfooded example.

## Why this is a separate Go module

A generator's needs must never reach the root module or any of its dependency-free packages
(`client`, `httpclient`, …). The hashes themselves are the root module's (`contract.HashDocument`,
over the hand-written RFC 8785 `internal/jcs`), so a generated client and a serving Go service hash
one way; this module adds `github.com/gowebpki/jcs` only to fuzz that canonicalizer against an
independent implementation (`jcsfuzz`). **A generator needing a dependency does not mean the code it
generates needs one**: a generated client's only import from this port is `client.Sender`,
`httpclient.Unmarshal`, and `benzene.Result[T]`/`benzene.Topic` - all already dependency-free.

It depends on the root module (`require github.com/daniellepelley/benzene-go`), resolved locally
through the root `go.work`. Build and test it with the rest of the workspace
(`go test $(scripts/modules.sh)`), or on its own:

```
cd codegen
go build ./...
go test -race -cover ./...
go test -fuzz=FuzzTransform ./jcsfuzz   # search for internal/jcs vs gowebpki/jcs differences
```

## Packages

| Package | What it is |
|---|---|
| `contractdoc` | Parses a Contract Document (rejecting an unknown `schemaDialect`, ignoring `benzene:` entries), applies the topic include-list (§5.2), walks the topic-scoped schema closure (§5.3), and hashes via the root module's `contract.HashDocument` (§6), recomputing both hashes on every projection. Deliberately generic-JSON-only (`map[string]any`) - every rule it implements is structural, so it needs no OpenAPI/JSON-Schema parsing library at all. |
| `gengo` | Turns a `contractdoc.Document` into Go source: schema → Go type mapping (`typename.go`), struct/interface emission (`types.go`), topic → Go identifier naming ported from the .NET reference (`naming.go`), and the two client shapes - `GenerateServiceClient` and `GenerateAtomicClients` (`client.go`/`atomic.go`). |
| `cmd/benzene-codegen` | The CLI (`build` subcommand). |
| `jcsfuzz` | Differential fuzz test of the root module's `internal/jcs` against `github.com/gowebpki/jcs` (test-only). |
| `conformance` | Runs `contractdoc` against the vendored `docs/specification/conformance/contract-document-cases.json` / `contract-hash-cases.json` fixtures (`../conformance/testdata/`, read by relative file path): the client-generator groups and every hash case, projections included. |

## Conformance

See [../conformance/README.md](../conformance/README.md)'s "Contract Document / contract hash
fixtures" section - these two fixture files are required only for a port that ships a client
generator, and this module is that generator.
