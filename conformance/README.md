# Conformance

`testdata/*.json` are vendored, byte-for-byte copies of the language-neutral fixtures from
[daniellepelley/Benzene](https://github.com/daniellepelley/Benzene)'s
`docs/specification/conformance/`. `conformance_test.go` is this port's runner - the
`test/Benzene.Conformance.Test/` project in the main repo is the reference for how a runner
consumes these files.

## Re-syncing

Copy the files below from the main repo whenever `docs/specification/conformance/`
changes there:

```
cp path/to/Benzene/docs/specification/conformance/status-vocabulary.json testdata/
cp path/to/Benzene/docs/specification/conformance/http-status-mapping.json testdata/
cp path/to/Benzene/docs/specification/conformance/envelope-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-descriptor-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-trace-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-collector-cases.json testdata/
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
| `conformance:panic` | (mesh trace cases only) Panics unconditionally - pins the panic→`ServiceUnavailable` trace rule |

## Mesh fixtures

`mesh-*.json` pin the mesh module (the main repo's `docs/specification/mesh.md` §7,
implemented here by the `mesh` and `meshd` packages); `mesh_conformance_test.go` is their
runner. Descriptor cases derive the service descriptor from the two canonical envelope
handlers and assert the derived schemas plus the descriptorHash's format/invariance/
sensitivity properties; trace cases assert the traceparent join/reject rules and the
invocation→semantic-status mapping; collector cases run ordered envelope sequences against a
fresh `meshd` collector per case. Mesh fixtures add one matching rule: arrays compare by
exact length with per-element subset matching, and an expected `[]` matches an
absent-or-empty actual array.
