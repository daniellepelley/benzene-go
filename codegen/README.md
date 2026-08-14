# codegen

`benzene-codegen` (`cmd/benzene-codegen`): generates a typed, topic-scoped Go client SDK from a
Benzene service's committed Contract Document (`{Service}.spec.json`) - the Go port of the .NET
repo's `Benzene.CodeGen.Client`. See [../docs/codegen-client.md](../docs/codegen-client.md) for the
full guide (CLI usage, the two output shapes, naming, type mapping, the `contractHash` algorithm,
and the `go:generate` pattern), and [../examples/codegen-helloworld](../examples/codegen-helloworld)
for a full worked, dogfooded example.

## Why this is a separate Go module

`contractHash` (`contract-document.md` §6) requires an RFC 8785 (JCS) canonicalizer -
`github.com/gowebpki/jcs`. That dependency must never reach the root module or any of its existing
dependency-free packages (`client`, `httpclient`, …); giving it its own module, exactly like
`awssqs`/`kafka`/`grpcbinding`/`diagnostics` do for their own third-party dependencies, is what keeps
it contained. **A generator needing a dependency does not mean the code it generates needs one**: a
generated client's only import from this port is `client.Sender`, `httpclient.Unmarshal`, and
`benzene.Result[T]`/`benzene.Topic` - all already dependency-free.

Unlike those sibling modules, `codegen` is deliberately **not** listed in the root `go.work`:
nothing the root module builds imports it (generated code is emitted as text, not linked against),
so there is nothing for a workspace-scoped `replace` to resolve. Build and test it directly:

```
cd codegen
go build ./...
go test -race -cover ./...
```

## Packages

| Package | What it is |
|---|---|
| `contractdoc` | Parses a Contract Document, applies the topic include-list / reserved-topic rules (§5.1-§5.2), walks the topic-scoped schema closure (§5.3), and computes `contractHash` (§6). Deliberately generic-JSON-only (`map[string]any`) - every rule it implements is structural, so it needs no OpenAPI/JSON-Schema parsing library at all. |
| `gengo` | Turns a `contractdoc.Document` into Go source: schema → Go type mapping (`typename.go`), struct/interface emission (`types.go`), topic → Go identifier naming ported from the .NET reference (`naming.go`), and the two client shapes - `GenerateServiceClient` and `GenerateAtomicClients` (`client.go`/`atomic.go`). |
| `cmd/benzene-codegen` | The CLI (`build` subcommand). |
| `conformance` | Runs `contractdoc` against the vendored `docs/specification/conformance/contract-document-cases.json` / `contract-hash-cases.json` fixtures (`../conformance/testdata/`, read by relative file path - no Go import of the root module). |

## Conformance

See [../conformance/README.md](../conformance/README.md)'s "Contract Document / contract hash
fixtures" section - these two fixture files are required only for a port that ships a client
generator, and this module is that generator.
