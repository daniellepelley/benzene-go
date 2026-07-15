# Conformance

`testdata/*.json` are vendored, byte-for-byte copies of the language-neutral fixtures from
[daniellepelley/Benzene](https://github.com/daniellepelley/Benzene)'s
`docs/specification/conformance/`. `conformance_test.go` is this port's runner - the
`test/Benzene.Conformance.Test/` project in the main repo is the reference for how a runner
consumes these files.

## Re-syncing

Copy the three files below from the main repo whenever `docs/specification/conformance/`
changes there:

```
cp path/to/Benzene/docs/specification/conformance/status-vocabulary.json testdata/
cp path/to/Benzene/docs/specification/conformance/http-status-mapping.json testdata/
cp path/to/Benzene/docs/specification/conformance/envelope-cases.json testdata/
```

`grpc-status-mapping.json` is intentionally **not** vendored - this port has no gRPC binding
yet, so there is nothing in this repo to run it against. Vendor and add a runner for it once a
`grpc` binding package exists.

## Canonical handlers

`envelope-cases.json` cases run against two handlers this test registers natively, per the
main repo's `conformance/README.md`:

| Topic | Behavior |
|---|---|
| `conformance:greet` | Returns `Ok` with `{"greeting": "Hello <name>"}` |
| `conformance:status` | Returns the given status verbatim, with `{"applied": "<status>"}` on success or the given errors on failure |
