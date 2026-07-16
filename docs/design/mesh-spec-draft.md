# Mesh wire contracts — DRAFT for promotion to the main repo's specification

**Status: DRAFT.** This is the Phase 5 deliverable that can be produced from this repo
(mesh.md §8): the mesh wire contracts written in the language-neutral style of
`docs/specification/`, ready to be lifted into the main Benzene repo as
`docs/specification/mesh.md` (plus conformance fixtures, sketched in §7). Until it lands
there, the main repo's spec remains the source of truth and this draft binds nothing.
The Go implementation (`mesh/`, `meshd/`) is the reference; where this draft and that
code disagree, fix whichever is wrong *before* promotion — after promotion, the spec wins.

Everything below follows the existing wire conventions: camelCase field names, flat
string→string headers, pre-serialized string bodies inside envelopes, the shared status
vocabulary (wire-contracts.md §3), and RFC 3339 timestamps.

---

## 1. Reserved topic: `mesh`

A mesh-enabled service intercepts the reserved topic `mesh` (plus app-chosen aliases),
exactly as `healthcheck` interception works (core-concepts.md §5): interception is by
topic id alone, ignoring version; any other topic passes through. The response payload is
the ServiceDescriptor (§2), status `Ok`.

Provisioning this endpoint is a deployment decision. A service that must not expose it
(e.g. pending security review) simply does not register the interception middleware —
every other mesh feed keeps working (§6).

## 2. ServiceDescriptor

The service's self-description, derived at startup from its handler registry — never
hand-maintained. Also the body of a `mesh:register` message.

```json
{
  "service": "orders",
  "serviceVersion": "1.4.2",
  "instanceId": "orders-7f9c",
  "runtime": "go",
  "binding": "http",
  "placement": { "cloud": "aws", "region": "eu-west-1" },
  "topics": [
    {
      "id": "order:create",
      "version": "v2",
      "requestSchema":  { "type": "object", "properties": { "name": { "type": "string" } }, "required": ["name"] },
      "responseSchema": { "type": "object", "properties": { "id":   { "type": "string" } }, "required": ["id"] }
    }
  ],
  "descriptorHash": "sha256:…",
  "degraded": ["registry"]
}
```

- `service` — required; the logical service name. Everything else is optional: a port
  MUST emit what it has and omit what it doesn't (empty values are omitted, not nulled).
- `runtime` — the implementing language/port identifier (`"go"`, `"dotnet"`, …).
- `binding` — the transport binding in use, when the service knows it.
- `placement.cloud` — detected or configured: `"aws"`, `"azure"`, `"gcp"`,
  `"self-hosted"`, or any explicit override. `region` is emitted only when the platform
  documents a way to know it; a port MUST NOT guess.
- `topics` — every registered topic, sorted by id then version. Derivation from the
  registry is the point of the contract: explicit registration (core-concepts.md §9)
  makes the registry the complete truth of what the service serves.
- `degraded` — names the feeds unavailable when the descriptor was built (currently
  `"registry"`), so a reduced descriptor is distinguishable from a service with no
  topics.

### 2.1 Schema derivation

`requestSchema`/`responseSchema` describe the **marshaled JSON form** of the registered
request/response types, as a subset of the JSON Schema 2020-12 vocabulary. A port derives
them once at startup. The mapping every port MUST follow (source language constructs on
the left are each port's equivalents):

| Construct | Schema |
|---|---|
| string | `{"type":"string"}` |
| boolean | `{"type":"boolean"}` |
| integer kinds | `{"type":"integer"}` |
| floating kinds | `{"type":"number"}` |
| timestamp type (marshals RFC 3339) | `{"type":"string","format":"date-time"}` |
| byte array (marshals base64) | `{"type":"string"}` |
| text-marshaling custom type | `{"type":"string"}` |
| raw/unknown JSON, dynamic values, custom serializers | `{}` (unconstrained) |
| nullable/pointer of T | T's schema with `"null"` added to its `type` |
| list/array of T | `{"type":"array","items":<T>}` |
| string-keyed map of T | `{"type":"object","additionalProperties":<T>}` |
| object/record | `{"type":"object","properties":{…},"required":[…]}` |

Object rules: serialization attributes/tags control names and omission exactly as the
port's JSON marshaler does; fields the marshaler always emits are listed in `required`
(declaration order — determinism feeds the hash); embedded/inherited members are
flattened the way the marshaler flattens them; recursive types are cut at the cycle with
`{}` (schemas stay self-contained; no `$ref`); constructs the marshaler cannot serialize
are `{}`.

### 2.2 descriptorHash

`"sha256:" + hex(sha256(canonicalJSON(descriptor)))` where the hashed descriptor has
`instanceId`, `degraded`, and `descriptorHash` itself blanked — the hash covers the
*contract* (identity, placement, topics, schemas), so two instances of one build hash
identically and the hash changes exactly when the contract changes. Canonical JSON:
object members in a fixed documented order (struct declaration order for fixed shapes,
lexicographic for maps), no insignificant whitespace.

## 3. TraceEvent

One pipeline invocation as the mesh sees it — semantic (topic + Benzene status), not
transport-shaped.

```json
{
  "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "spanId": "00f067aa0ba902b7",
  "parentSpanId": "0af7651916cd43dd",
  "service": "orders",
  "instanceId": "orders-7f9c",
  "topic": "order:create",
  "topicVersion": "v2",
  "status": "ValidationError",
  "durationMs": 12.4,
  "startedAt": "2026-07-16T09:14:03.120Z",
  "correlationId": "abc-123"
}
```

- `traceId`/`spanId`/`parentSpanId` are the W3C Trace Context fields (32/16/16 lowercase
  hex). An inbound `traceparent` header joins the existing trace (its trace-id adopted,
  its parent-id recorded); absent or malformed → a fresh trace-id, no parent. All-zero
  ids are invalid per the W3C spec and treated as absent.
- Outbound propagation: a handler making a downstream Benzene call SHOULD forward
  `traceparent: 00-<traceId>-<spanId>-01` built from its own invocation's span.
- `status` is the Benzene status verbatim; empty only when no downstream middleware
  produced a result (a wiring gap, reported as-is).
- `correlationId` mirrors the `x-correlation-id` header when present.
- Coverage MUST be structural: because a port's router already converts missing
  handlers, conversion failures, and handler panics/exceptions into results
  (core-concepts.md §5), every routed invocation yields exactly one TraceEvent.

## 4. Collector ingest topics

A collector is an ordinary Benzene service serving these topics over any envelope-capable
transport:

| Topic | Body | Success payload |
|---|---|---|
| `mesh:register` | ServiceDescriptor (§2) | `{"accepted":1}` |
| `mesh:heartbeat` | Heartbeat (§5) | `{"accepted":1}` |
| `mesh:traces` | `{"events":[TraceEvent…]}` | `{"accepted":<count>}` |

Validation: `service` (register/heartbeat) is required → `BadRequest` when missing. A
`mesh:traces` batch of any size, including empty, is accepted.

Re-registration replaces the previous registration wholesale, including provider edges —
a redeploy that drops a topic drops the claim to provide it.

Sender behavior (normative for ports): trace export MUST be asynchronous, non-blocking
and lossy under backpressure — a full buffer drops events, a failed send drops the batch,
and no mesh feed may ever fail, slow, or block the invocation it observed.

## 5. Heartbeat

The health-check aggregate response (wire-contracts.md §5) reused byte-for-byte, wrapped
with identity:

```json
{
  "service": "orders",
  "instanceId": "orders-7f9c",
  "descriptorHash": "sha256:…",
  "sentAt": "2026-07-16T09:14:03Z",
  "health": { "isHealthy": true, "healthChecks": { "db": { "status": "ok", "type": "postgres" } } }
}
```

A heartbeat's `descriptorHash` differing from the registered descriptor's hash means the
instance runs a contract the collector hasn't learned — the collector MUST surface this
(the Go collector reports per-instance `hashMatches`) rather than silently keeping stale
topics.

## 6. Degradation (normative)

Every mesh feed — descriptor endpoint, registration, heartbeats, traces — is independent
and optional, on both sides:

- **Service side**: an unprovisioned descriptor endpoint, an unreachable collector, a
  failing exporter, or an absent registry each reduce the mesh and MUST NOT affect the
  service's own traffic in any way.
- **Collector side**: partial fleets are accepted and rendered as reduced. Traces from an
  unregistered service present it as known-but-reduced (missing descriptor feed); a
  registered service with no traffic is a catalog entry with no stats; no heartbeats
  means unknown health. A missing feed MUST NOT fail ingestion or queries.

## 7. Conformance fixtures (sketch)

To be authored alongside promotion, in the style of the existing
`docs/specification/conformance/` fixtures, and vendored back into each port:

- `mesh-descriptor-cases.json` — given a canonical set of registered topics/types (the
  conformance fixture types), the exact descriptor JSON (including schemas and hash) a
  port must produce. This pins §2.1's mapping cross-language: a C# and a Go port must
  emit byte-identical descriptors for equivalent registrations.
- `mesh-trace-cases.json` — traceparent join/reject cases (§3) and the invocation→status
  mapping (success, missing handler, panic).
- `mesh-collector-cases.json` — ingest validation, re-registration replacement, consumer
  derivation from parentage, and the degradation matrix of §6.

## 8. Out of scope (this draft)

Query read-model shapes (`mesh:query:*`) are implemented by the Go collector but
deliberately left out of the promoted contract for now: they are one collector's read
models, not cross-port interop surfaces. They join the spec if/when a second collector
implementation or third-party view needs them pinned.
