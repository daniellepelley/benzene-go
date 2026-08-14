# Generated clients (benzene-codegen)

`benzene-codegen` turns a service's committed Contract Document (`{Service}.spec.json`, produced by
that service's own port — the .NET `Benzene.Descriptor`, or this port's own mesh descriptor, see
[the specification](https://benzene.app/docs/specification/contract-document.html)) into a typed Go
client: one struct per request/response DTO, one method per topic, an embedded contract hash, and a
`RequiredTopics` list — with **zero transport dependency**. It works against a Contract Document
produced by *any* Benzene port, not only this one: a Go consumer of a .NET, TypeScript, or Python
service gets the same typed, topic-scoped client this page describes.

This is a Go port of the .NET repo's `Benzene.CodeGen.Client` (`benzene build`), implementing the
same language-neutral generation rules — topic scoping, reserved-topic exclusion, the schema-closure
walk, and the `contractHash` algorithm — from
[`contract-document.md`](https://benzene.app/docs/specification/contract-document.html). Method
naming and file layout are **not** part of that specification (§5.5): the choices below are this
port's own idiom, not a promise every language port makes the same ones.

## Where it lives, and why

The generator (`codegen/`) is a **separate Go module** from the one this documentation otherwise
describes, with its own `go.mod`: `github.com/daniellepelley/benzene-go/codegen`. It needs a JSON
canonicalizer (`github.com/gowebpki/jcs`, for the `contractHash` algorithm's RFC 8785 step) — a
dependency that must never leak into the root module or any of its dependency-free packages
(`client`, `httpclient`, …), matching this repo's `awssqs`/`kafka`/`grpcbinding` pattern of giving a
third-party dependency its own module. **A client generator has this dependency; the runtime it
generates code for does not** — a generated client's only import from this port is `client.Sender`,
`httpclient.Unmarshal`, and `benzene.Result[T]`/`benzene.Topic`, all already dependency-free.

```
codegen/
  contractdoc/   parses a Contract Document, applies topic scoping, walks the schema closure,
                 computes contractHash — generic-JSON-only, no OpenAPI/JSON-Schema library needed
  gengo/         Go code generation: type mapping, naming, the two client shapes below
  cmd/
    benzene-codegen/   the CLI
```

## The CLI

```
go run github.com/daniellepelley/benzene-go/codegen/cmd/benzene-codegen build \
  -file contracts/payments.spec.json \
  -out ./paymentsclient \
  -service Payments \
  -package paymentsclient
```

| Flag | Meaning |
|---|---|
| `-file` | path to the Contract Document (required) |
| `-out` | output directory (required) |
| `-mode` | `service` (default, see below) or `topic` |
| `-service` | service name → `{Service}Client`/`New{Service}Client` (mode=`service`) |
| `-package` | generated Go package name, used exactly — must be a legal Go identifier, or the CLI fails loud |
| `-topics` | comma-separated topic include-list (`contract-document.md` §5.2) |
| `-include-reserved` | admit reserved `benzene:*` topics when `-topics` is not given |

The CLI exits non-zero — with a message on stderr naming the problem — on an unknown flag, an
unparseable document, or an include-listed topic the document doesn't have (the error names both the
unknown topic(s) and the document's actual topics, per §5.2's fail-loud rule).

## Two output shapes

**Service client** (`-mode service`, the default) — one Go type covering every in-scope topic:

```go
const ContractHash = "sha256:..."

var RequiredTopics = []benzene.Topic{{ID: "payments:capture"}, {ID: "payments:get-all"}}

type PaymentsClient struct{ /* ... */ }

func NewPaymentsClient(sender client.Sender) *PaymentsClient
func (c *PaymentsClient) CapturePayments(ctx context.Context, req CapturePayment) (benzene.Result[PaymentDto], error)
func (c *PaymentsClient) CapturePaymentsWithHeaders(ctx context.Context, req CapturePayment, headers map[string]string) (benzene.Result[PaymentDto], error)
```

By default this covers a service's **domain topics only** — reserved Benzene utility topics
(`benzene:spec`, `benzene:healthcheck`, `benzene:mesh`, …) are excluded whether or not they carry an
explicit `"reserved": true` flag (`contract-document.md` §5.1's flag-OR-prefix rule). `-topics` scopes
`RequiredTopics`/the generated methods to just the named topics; `components`/DTOs are **not**
narrowed by an include-list (§5.2) — every schema in the document is still generated, even one
reachable only from an excluded topic, matching the reference implementation exactly.

**Topic (atomic) client** (`-mode topic`) — one **self-contained** client per topic, in its own
package, with `components` narrowed to exactly that topic's [schema closure](#the-schema-closure)
(`contract-document.md` §5.3):

```
go run .../benzene-codegen build -file contracts/payments.spec.json -out . -mode topic -topics payments:capture
# -> ./paymentscapture/{client.go,types.go}
```

Each topic client gets its **own directory/package** (`{clientName}`, derived from the topic via
`TopicMethodName` below) so two independently-generated topic clients can never collide on a shared
DTO name — mirroring the .NET reference's per-client namespace/folder. Its embedded `ContractHash`
covers only that topic's projection: an explicitly-requested reserved topic survives in an atomic
client's hash (only its `reserved` flag is stripped, not the entry) since asking for it by name makes
it part of what's being hashed — see `contract-document.md` §6.2/§6.4 for the full rule and why a
service-level hash and a topic-scoped hash are never comparable to each other.

## Naming

- **Package name**: used exactly as given (`-package`), validated as a legal Go identifier.
- **Service client method name**: the reversed-topic convention (`TopicReversedMethodName`, ported
  from the .NET reference exactly — including that it Pascal-cases only a segment's first
  character, not every word): split the topic on `:`, reverse the segments, format each, concatenate.
  `payments:capture` → `CapturePayments`.
- **Topic client package/type name**: the non-reversed convention (`TopicMethodName`): same
  formatting, segments in original order. `payments:capture` → `PaymentsCapture` (package
  `paymentscapture`).
- **Schema/property names**: PascalCased into a Go identifier (`FormatGoName`), but the **wire**
  name — the JSON property key — is kept byte-for-byte verbatim in the `json:"..."` tag. A topic
  string is likewise never re-cased anywhere it appears as a string literal.

## Type mapping

Plain Go structs with `json:"..."` tags, no validation library — matching this repo's existing
hand-written style (see `examples/http-helloworld`'s `greetRequest`), not the .NET reference's
`[ExcludeFromCodeCoverage]`-decorated classes.

| Schema | Go |
|---|---|
| `$ref` | the referenced schema's own Go type name |
| `"type": "string"` | `string` (`format: "date-time"` → `time.Time`; `format: "uuid"` → `string` — Go has no built-in UUID type, and this generator does not want to force a UUID library on every generated client, unlike .NET's built-in `Guid`) |
| `"type": "integer"` | `int` (`format: "int64"` → `int64`) |
| `"type": "number"` | `float64` (no format-driven precision heuristics — the schema's declared type governs, matching the reference) |
| `"type": "boolean"` | `bool` |
| `"type": "array"`, `items` | `[]ItemType` |
| `"type": "object"`, schema-valued `additionalProperties` | `map[string]ValueType` |
| `allOf` (one `$ref` branch + inline properties) | Go struct **embedding**: the base type is embedded anonymously, so `encoding/json` flattens it on marshal/unmarshal — the honest Go equivalent of a C# base class, with no inheritance keyword needed |
| `oneOf` + `discriminator` | an **unexported marker-method interface** (`type Pet interface { isPet() }`) plus an `isPet()` method on each subtype the discriminator's mapping names — Go has no union type, so this seals the interface to exactly the declared subtypes rather than faking generic polymorphism |
| a required property (schema's own `required[]`, and not itself `nullable: true`) | a non-pointer field, no `omitempty` |
| an optional or `nullable: true` property | a pointer field (a slice/map field stays unwrapped — already nil-able — just `omitempty`) |

The required/optional pointer split is this Go port's own addition (the .NET reference has no
concept of it — C# reference types are nullable by default); it follows this repo's own struct/tag
convention, not a promise the specification makes.

## The schema closure

A topic client's `components` is exactly the set `contract-document.md` §5.3 defines: walk the
topic's `request`/`response` schemas through `$ref`/`items`/`additionalProperties` (only when itself
a schema, not a boolean)/`properties`/`allOf`/`anyOf`/`oneOf`, cycle-safe (a `$ref` is only walked the
first time it's reached — the same rule that terminates a two-schema reference cycle). This walk is
pinned byte-for-byte by `docs/specification/conformance/contract-document-cases.json`'s
`schemaClosureCases`, vendored into this repo's `conformance/testdata/` and run by
`codegen/conformance/conformance_test.go`.

## The `contractHash`

Every generated client embeds a `contractHash` (`contract-document.md` §6):

```
"sha256:" + lowercase-hex(sha256(canonicalJSON(normalize(document))))
```

`canonicalJSON` is RFC 8785 (JCS), via `github.com/gowebpki/jcs` — the only third-party dependency in
the whole `codegen` module. `normalize` strips every `example`, the top-level `messageEndpoint`/
`transports`, and the `reserved` flag itself off every surviving request; a **whole-service or
service-level** document additionally drops every request §5.1 detects as reserved *entirely* — a
**topic-scoped** (atomic) document does not, if it was explicitly built for that one reserved topic.
`codegen/conformance/conformance_test.go` runs this against
`docs/specification/conformance/contract-hash-cases.json`'s cases byte-for-byte, including the two
cases proving normalization actually strips decoration rather than hashing it verbatim.

Compare a client's embedded `ContractHash` against the producing service's own served contract hash
(e.g. its `benzene:mesh` descriptor or `/benzene/spec` document) to detect drift between the client
you hold and the service you're calling — the same comparison `clienthealthcheck.ServiceCheck`
performs, using `WithExpectedContractHash` to supply the value this generator would have embedded.

## The `go:generate` pattern

Generated code is **committed**, matching Go's own `go generate` convention (the tool runs at
development time, never at build time). `examples/codegen-helloworld` is the worked example:

```go
//go:generate sh generate.sh
```

(a small wrapper script, not an inline `go run ../../codegen/cmd/benzene-codegen` — the generator
lives in a separate module with its own `go.mod`, so a plain relative `go run` path can't resolve it
from a directory in the *root* module; the script `cd`s into the generator module and calls back with
the caller's own absolute directory, so `-file`/`-out` still resolve relative to the example).

CI (or a pre-merge check in your own repo) should run:

```
go generate ./... && git diff --exit-code
```

— regenerating and diffing catches a Contract Document that changed without its generated client
being regenerated and re-committed to match. `codegen/gengo/dogfood_committed_test.go` reproduces
this exact check as an ordinary `go test`, so a generator change that would silently invalidate the
example's committed output fails in CI immediately rather than only on the next manual regeneration.

## Wiring a generated client

There is no DI container reflection in this port (`client/di.go`'s `RegisterSender`/
`SenderFromScope`, not a declarative outbound-routing table) — so a generated client is just a
constructor over `client.Sender`:

```go
sender := httpclient.NewClient("https://payments.internal/benzene/invoke")
payments := paymentsclient.NewPaymentsClient(sender)

result, err := payments.CapturePayments(ctx, paymentsclient.CapturePayment{
	OrderId:  "order-123",
	Currency: "GBP",
})
if err != nil {
	// a genuine local fault before the send even happened (e.g. JSON marshal failure) - not a
	// domain outcome
}
if !result.IsSuccessful() {
	// result.Status / result.Errors - never a Go error for a domain failure
}
```

`RequiredTopics` is a plain exported `[]benzene.Topic` — free, self-documenting, with nothing to wire
it into (this port's `client` package deliberately has no container-wide outbound-routing validation
to register it with, see `client/`'s package doc); use it for your own startup checks if you want one.

See `examples/codegen-helloworld` for the full worked example, including a fake-`client.Sender` test
proving the generated method sends the right topic with the right typed payload.
