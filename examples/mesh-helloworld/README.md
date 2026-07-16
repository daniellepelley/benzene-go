# mesh-helloworld

The whole Benzene Mesh story ([`docs/design/mesh.md`](../../docs/design/mesh.md), Phases
1-4) running in one process: a `meshd` collector plus two meshed services - **greeter**
(serves `greet`) and **frontdoor** (serves `welcome`, and calls greeter over the wire
envelope, propagating its trace span).

```
go run .
```

| Port | What |
|---|---|
| `8090` | meshd: the Mesh View at `/`, the collector's envelope endpoint at `/invoke` |
| `8080` | greeter: `/greet`, `/health`, `/invoke` |
| `8081` | frontdoor: `/welcome`, `/health`, `/invoke` |

Generate some traffic and watch the view at <http://localhost:8090/>:

```
curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'
```

Everything the view shows is **derived** from the running services, nothing is declared:

- both services' rows come from their `mesh:register` descriptors (topics + schemas from
  the Registry) and turn healthy on their heartbeats;
- the `greet` topic's *consumers: frontdoor* comes from trace parentage - frontdoor's
  handler forwards `mesh.SpanFromContext(ctx)` as a `traceparent` header (see
  `welcomeHandler`), which is the only mesh-specific line an application handler ever
  writes;
- the recent-flows list joins both services' trace events into one cross-service flow.

## Degradation demo

Every mesh feed is optional (the design's core rule - an unavailable feed reduces the
mesh, never the service). Things to try:

- Stop nothing, just delete the `mesh.Middleware(descriptor)` line in `newService` and
  restart: the service still runs and still traces, but its row degrades to
  *reduced feeds: descriptor* - anonymous but live.
- Point `meshdEndpoint` at a port where nothing listens: both services keep serving
  traffic untouched (`TestReducedMeshStillServes` proves this in CI); registration and
  heartbeats log a line and move on, and the trace feed drops its batches.

## What is and isn't verified

`go test ./...` runs the full story over real HTTP loopback servers (collector + both
services + a cross-service call, then asserts the derived fleet, consumer edge, and
flow). No cloud deployment is involved; to deploy the pieces to Lambda/Functions/
Cloud Run, apply the corresponding `*-helloworld` example's deploy steps to each service
- a meshed service is an ordinary Benzene service.

The heartbeat sent here reports a static healthy status to keep the example small; a
real service would run the same checks its `healthcheck` middleware serves.
