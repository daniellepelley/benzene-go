# codegen-helloworld

Dogfoods `benzene-codegen` (`codegen/cmd/benzene-codegen`, see
[docs/codegen-client.md](../../docs/codegen-client.md)) against a real, committed Contract
Document: `contracts/payments.spec.json`, vendored verbatim from the .NET reference implementation's
own example (`examples/AwsMesh/Orders/contracts/payments.spec.json` in
[benzene-dotnet](https://github.com/daniellepelley/benzene-dotnet)). It proves the same document a
.NET service commits produces a working, typed Go client with no .NET SDK involved.

## Layout

```
contracts/payments.spec.json   the committed Contract Document (unmodified, from the .NET reference)
doc.go                          package doc + the //go:generate directive
generate.sh                     what //go:generate actually runs (see its own comment for why)
paymentscapture/                GENERATED, COMMITTED: a self-contained client for payments:capture only
main.go                         a runnable illustration - wires the generated client to a real HTTP endpoint
main_test.go                    the actual dogfood proof - a fake client.Sender, no live service needed
```

`payments.spec.json` has three request/response topics: `payments:capture`, `payments:get-all`, and
the reserved `benzene:spec` (flagged `"reserved": true`). Generating a **topic-scoped** client for
`payments:capture` only exercises both of contract-document.md §5's core rules at once: the reserved
topic is excluded from scope by default, and the schema closure narrows `components` down to just
`CapturePayment`/`PaymentDto` - `payments:get-all`'s and `benzene:spec`'s own schemas
(`SpecRequest`/`RawStringMessage`) never appear in the generated output.

## Regenerating

```
cd examples/codegen-helloworld
go generate ./...
git diff --exit-code   # should be empty if contracts/payments.spec.json didn't change meaningfully
```

The generated files under `paymentscapture/` are **committed** - this is Go's ordinary
`go:generate` convention (the tool runs at development time, not build time), not something specific
to this example. A CI job for a real service would run the same `go generate ./... && git diff
--exit-code` to catch a contract document that changed without its generated client being
regenerated to match; `codegen/gengo/dogfood_committed_test.go` reproduces that exact check as an
ordinary `go test` against this example specifically, so a generator regression is caught immediately
rather than only on the next manual `go generate`.

## Running the demo

```
go run . -endpoint http://localhost:8080/benzene/invoke -order-id order-123 -amount 42.42 -currency GBP
```

This needs a running Benzene service (any port) that registers a `payments:capture` handler at that
envelope endpoint - there is no such service running in this repo's CI, so this command is
illustrative, not exercised automatically. `go test ./...` is the actual verification, and needs
nothing running.
