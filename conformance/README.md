# Conformance

`testdata/*.json` are vendored, byte-for-byte copies of the language-neutral fixtures from
[daniellepelley/Benzene](https://github.com/daniellepelley/Benzene)'s
`docs/specification/conformance/`. `conformance_test.go` is this port's runner - the
`test/Benzene.Conformance.Test/` project in the main repo is the reference for how a runner
consumes these files. `SPEC_VERSION` records the main repo commit these fixtures (and this port's
understanding of the spec generally) were last synced against.

## Re-syncing

Copy the files below from the main repo whenever `docs/specification/conformance/`
changes there, and update `SPEC_VERSION` to the main repo's new `HEAD` commit:

```
cp path/to/Benzene/docs/specification/conformance/status-vocabulary.json testdata/
cp path/to/Benzene/docs/specification/conformance/http-status-mapping.json testdata/
cp path/to/Benzene/docs/specification/conformance/grpc-status-mapping.json testdata/
cp path/to/Benzene/docs/specification/conformance/envelope-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-descriptor-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-trace-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-collector-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/mesh-issue-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/transport-metadata-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/contract-document-cases.json testdata/
cp path/to/Benzene/docs/specification/conformance/contract-hash-cases.json testdata/
git -C path/to/Benzene rev-parse HEAD > SPEC_VERSION
```

## Canonical handlers

`envelope-cases.json` cases run against two handlers this test registers natively, per the
main repo's `conformance/README.md`:

| Topic | Behavior |
|---|---|
| `conformance:greet` | Returns `Ok` with `{"greeting": "Hello <name>"}` |
| `conformance:status` | Returns the given status verbatim, with `{"applied": "<status>"}` on success or the given errors on failure |
| `conformance:panic` | (mesh trace cases only) Panics unconditionally - pins the panic→`service-unavailable` trace rule |

## Mesh fixtures

`mesh-*.json` pin the mesh module (the main repo's `docs/specification/mesh.md` §7,
implemented here by the `mesh` and `meshd` packages); `mesh_conformance_test.go` is their
runner. Descriptor cases derive the service descriptor from the two canonical envelope
handlers and assert the derived schemas plus the descriptorHash's format/invariance/
sensitivity properties; trace cases assert the traceparent join/reject rules and the
invocation→semantic-status mapping; collector cases run ordered envelope sequences against a
fresh `meshd` collector per case. `mesh-issue-cases.json` pins the issue-feed collector
(`mesh.md` §4.1: batch-service required, empty-batch liveness, delta-merge by fingerprint,
invalid-entry skip, and the conditional `issues` missing-feed marker); it shares the collector
cases' step/assertion model exactly and reuses the same runner. Mesh fixtures add one matching
rule: arrays compare by exact length with per-element subset matching, and an expected `[]`
matches an absent-or-empty actual array.

## Transport metadata fixture

`transport-metadata-cases.json` pins the native-metadata topic resolution of the main repo's
`wire-contracts.md` §2 - the reserved `topic` key (tier A, configurable), the case-insensitive
read, the remaining-metadata-becomes-headers rule, the "only the configured key routes" guard,
and that an overridden key is honoured. `transport_metadata_conformance_test.go` runs it against
`wire.ResolveMetadataTopic`, the shared primitive every native-metadata inbound binding
(`awssqs`, `awssns`, `gcppubsub`) delegates to. The `version-travels-alongside-the-topic` case
carries `requires: versioning` and is skipped: `benzene-version` is tier C (payload versioning),
which this port does not implement (see `ROADMAP.md`).

## Contract Document / contract hash fixtures

`contract-document-cases.json` and `contract-hash-cases.json` pin the main repo's
`docs/specification/contract-document.md` - the Contract Document format, its topic-scope and
schema-closure generation semantics (§5), and the cross-language `contractHash` algorithm (§6).
They are required only for a port that ships a client generator (the same conditional shape as the
mesh/collector fixtures above); this port's generator lives in the separate `codegen` Go module
(its own dependency on `github.com/gowebpki/jcs` for RFC 8785 canonicalization would otherwise leak
into this, the dependency-free, module - see `codegen/README.md` and `docs/codegen-client.md`).
Its `codegen/conformance/conformance_test.go` is the runner for these two files, reading them from
this directory by relative file path rather than a Go import, so the generator module needs no
dependency on this one to run them.
