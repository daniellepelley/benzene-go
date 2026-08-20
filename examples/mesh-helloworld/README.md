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
| `8080` | greeter (`greet`) | **all**: descriptor endpoint, registration, heartbeats, traces, issues |
| `8081` | frontdoor (`welcome`, calls greeter) | **all** |
| `8082` | legacy-portal (`legacy:relay`, calls greeter) | **traces + issues only** - no descriptor endpoint, no registration, no heartbeats |

Open <http://localhost:8090/> and generate traffic:

```
curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'     # fully meshed cross-service flow
curl -s -X POST localhost:8082/relay   -d '{"name":"Mesh"}'     # the same flow via the reduced service
```

## What each part demonstrates

The topic catalog (who provides/consumes what) is **declared** by each service's own descriptor;
health, traffic stats, and the observed-activity/drift signals layered on top are **derived**
from what the running services actually do (spec §4, §4.2 - the main repo's 2026-08 revision):

- **Descriptor + schemas + contract hash** (spec §2): each fully-meshed service's row comes
  from its `benzene:mesh:register` descriptor - `topics` from the Registry (what it provides),
  `consumes` from the OutboundRegistry (what it consumes, spec §2.3), request/response JSON
  Schemas derived at startup from the registered types, and the `descriptorHash`. Fetch one
  directly from the reserved `mesh` topic:

  ```
  curl -s -X POST localhost:8080/benzene/invoke -d '{"topic":"benzene:mesh","headers":{},"body":""}'
  ```

- **Health from heartbeats** (spec §5): greeter and frontdoor turn *healthy* on their
  heartbeats (which carry the descriptor hash, so a redeployed instance with a changed
  contract would show a hash mismatch in `benzene:mesh:query:service`).
- **The consumer edge is declared, not trace-derived** (spec §2.3, §4): frontdoor's
  `newService` call registers an outbound record for `greet` (`mesh.RegisterOutbound` in
  `main.go`) - that, and only that, is why `greet`'s topic row shows *consumers: frontdoor*,
  and it would show it even with zero traffic. frontdoor's outbound client is separately
  wrapped in `mesh.WithTraceContext`, forwarding the current invocation's span as a
  `traceparent` header - but that propagation only lets the collector show the *already-declared*
  edge as observed (spec §4.2); it plays no part in putting the edge on the graph. legacy-portal
  calls greeter exactly as much, but never registers, so it never appears as a consumer no
  matter how much traffic it sends - a live demonstration that the graph is knowable before a
  single message flows, and that traffic alone can't add to it.
- **Issue feed** (spec §4.1): every service also runs `mesh.IssueMiddleware` with a
  `mesh.PushIssueExporter`, so a failing invocation - e.g. `curl -X POST localhost:8081/welcome
  -d '{"name":""}'`, where greeter answers `bad-request` - is classified and deduplicated by
  fingerprint at the source and pushed to the collector, which merges it (delta counts) and
  surfaces it on the fleet view. An empty batch flushes on the interval as the feed's liveness,
  so a quiet wired service is distinguishable from an unwired one. The collector itself also
  derives `contract-drift` issues (spec §4.1, §4.2) when a *registered* service's traces name a
  topic it didn't declare - legacy-portal is exempt from this too, since an anonymous service has
  no declared contract to diverge from.
- **Degradation, live** (spec §6): legacy-portal provisions only the trace and issue feeds
  (`provisionDescriptor=false` in `main.go`, never announces or heartbeats). It serves
  traffic like any other service, its calls still produce flows and add to `greet`'s
  invocation stats, and its row reads *reduced feeds: descriptor, health* - anonymous-but-live,
  never an error, and never a consumer edge or a drift flag either (it has nothing registered
  to derive either from).
- **Drill-downs** - the same read models the view uses:

  ```
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"benzene:mesh:query:service","headers":{},"body":"{\"service\":\"greeter\"}"}'
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"benzene:mesh:query:topic","headers":{},"body":"{\"topic\":\"greet\"}"}'
  curl -s -X POST localhost:8090/benzene/invoke -d '{"topic":"benzene:mesh:query:trace","headers":{},"body":"{\"traceId\":\"<id from the view>\"}"}'
  ```

## What is and isn't verified

`go test ./...` runs the full story over real HTTP loopback servers: collector + all three
services, a meshed and a reduced cross-service call, then asserts the derived fleet (health,
missing-feed markers on legacy-portal), the descriptor served on the reserved topic
(schemas + hash), the declared consumer edge (frontdoor only - legacy-portal's traffic counts
in the stats but leaves no edge or drift flag), the joined flows, and the parent-child span
relationship via the trace drill-down. A second test points a fully-meshed service at a dead
collector port and proves announce/heartbeat log-and-continue while the service keeps
serving - the degradation rule end to end. The mesh wire shapes themselves are additionally
pinned by the vendored fixtures in `conformance/` (see `mesh-*.json`).

No cloud deployment is involved; to deploy the pieces to Lambda/Functions/Cloud Run, apply
the corresponding `*-helloworld` example's deploy steps to each service - a meshed service
is an ordinary Benzene service.

The heartbeat sent here reports a static healthy status to keep the example small; a real
service would run the same checks its `healthcheck` middleware serves.
