# mesh-helloworld

The whole Benzene Mesh story ([`docs/design/mesh.md`](../../docs/design/mesh.md), all
phases - including the wire contracts promoted to the main repo's spec as
`docs/specification/mesh.md`) running in one process: a `meshd` collector and three
services, one of them deliberately reduced.

```
go run .
```

| Port | Service | Mesh feeds provisioned |
|---|---|---|
| `8090` | meshd | the Mesh View at `/benzene/fleet-ui` (`/` redirects there), the collector's envelope endpoint at `/benzene/invoke` |
| `8080` | greeter (`greet`) | **all**: descriptor endpoint, registration, heartbeats, traces |
| `8081` | frontdoor (`welcome`, calls greeter) | **all** |
| `8082` | legacy-portal (`legacy:relay`, calls greeter) | **traces only** - no descriptor endpoint, no registration, no heartbeats |

Open <http://localhost:8090/> and generate traffic:

```
curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'     # fully meshed cross-service flow
curl -s -X POST localhost:8082/relay   -d '{"name":"Mesh"}'     # the same flow via the reduced service
```

## What each part demonstrates

Everything the view shows is **derived** from the running services, nothing is declared:

- **Descriptor + schemas + contract hash** (spec §2): each fully-meshed service's row comes
  from its `mesh:register` descriptor - topics from the Registry, request/response JSON
  Schemas derived at startup from the handler types, and the `descriptorHash`. Fetch one
  directly from the reserved `mesh` topic:

  ```
  curl -s -X POST localhost:8080/benzene/invoke -d '{"topic":"mesh","headers":{},"body":""}'
  ```

- **Health from heartbeats** (spec §5): greeter and frontdoor turn *healthy* on their
  heartbeats (which carry the descriptor hash, so a redeployed instance with a changed
  contract would show a hash mismatch in `mesh:query:service`).
- **Consumer edges from trace parentage** (spec §3-4): the `greet` topic shows
  *consumers: frontdoor, legacy-portal* because each caller forwards
  `mesh.SpanFromContext(ctx)` as a `traceparent` header (see `welcomeHandler`) - the only
  mesh-specific line an application handler ever writes.
- **Degradation, live** (spec §6): legacy-portal provisions only the trace feed
  (`provisionDescriptor=false` in `main.go`, never announces or heartbeats). It serves
  traffic like any other service, its calls still produce flows and consumer edges, and
  its row reads *reduced feeds: descriptor, health* - anonymous-but-live, never an error.
- **Drill-downs** - the same read models the view uses:

  ```
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"mesh:query:service","headers":{},"body":"{\"service\":\"greeter\"}"}'
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"mesh:query:topic","headers":{},"body":"{\"topic\":\"greet\"}"}'
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"mesh:query:trace","headers":{},"body":"{\"traceId\":\"<id from the view>\"}"}'
  ```

## What is and isn't verified

`go test ./...` runs the full story over real HTTP loopback servers: collector + all three
services, a meshed and a reduced cross-service call, then asserts the derived fleet (health,
missing-feed markers on legacy-portal), the descriptor served on the reserved topic
(schemas + hash), both consumer edges, the joined flows, and the parent-child span
relationship via the trace drill-down. A second test points a fully-meshed service at a dead
collector port and proves announce/heartbeat log-and-continue while the service keeps
serving - the degradation rule end to end. The mesh wire shapes themselves are additionally
pinned by the vendored fixtures in `conformance/` (see `mesh-*.json`).

No cloud deployment is involved; to deploy the pieces to Lambda/Functions/Cloud Run, apply
the corresponding `*-helloworld` example's deploy steps to each service - a meshed service
is an ordinary Benzene service.

The heartbeat sent here reports a static healthy status to keep the example small; a real
service would run the same checks its `healthcheck` middleware serves.
