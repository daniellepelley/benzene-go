# azurecosmos

The self-hosted **Azure Cosmos DB Change Feed** binding for [benzene-go](https://github.com/daniellepelley/benzene-go)
— a `Worker` that opens the change feed itself and dispatches each delivered batch of changed
documents through a Benzene pipeline. It is the Go port of the main repo's `Benzene.Azure.CosmosDb`
(the self-hosted, non-Functions flavor).

It lives in its **own Go module** because it depends on the Azure Cosmos DB SDK
(`github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos`), keeping the root `benzene-go` module
zero-dependency. See the repo's `RELEASING.md` for the multi-module layout.

## What it is

This is the standalone-compute counterpart of the zero-dependency
[`azurefunctions.CosmosHandler`](../azurefunctions). There, the Azure Functions host owns the
change-feed connection and lease container; here, **your worker owns the change-feed connection**
(a container, a Kubernetes deployment, a VM running its own poll loop). The two differ only in who
owns the feed and the durable position — not in how a batch is dispatched.

Like `CosmosHandler`, it is **fan-in, not topic-routed** (core-concepts §3, streaming-shaped): the
whole delivered batch of changed documents is **one** pipeline invocation — not one per document —
dispatched to the single topic you name in `Worker.Topic`. Its handler receives the batch as a
slice:

```go
benzene.Register(registry, benzene.NewTopic("orders:changed"),
    benzene.Handler[[]OrderDocument, Ack](func(ctx context.Context, orders []OrderDocument) benzene.Result[Ack] {
        // process the whole batch; return a failure Result to redeliver it
        return benzene.Ok(Ack{})
    }))
```

The dispatch body is the JSON array of changed documents, and the only synthesized header is
`cosmos-document-count` — exactly what `CosmosHandler` produces (`envelope.DispatchTopicResult`
under the hood).

## Checkpointing is the caller's responsibility

The Cosmos change feed is resumed by a **continuation token**, and coordinating that token durably
across worker instances is done through an **application-owned lease container** (the same machinery
the Change Feed Processor and the Functions trigger rely on). That lifecycle is heavy and
application-owned, so — exactly as the sibling [`azureeventhub.Consumer`](../azureeventhub) defers
Event Hubs' durable checkpoint store — this `Worker` deliberately does **not** implement a lease
container, partition ownership, or load-balancing.

Instead it is scoped to the poll-and-dispatch loop over the narrow `ChangeFeedReader` interface. You
supply:

- the **reader** (`NewChangeFeedReader`, wrapping a real `*azcosmos.ContainerClient`),
- an optional `StartContinuation` to resume from (loaded from your durable store), and
- an optional `Checkpoint` hook the `Worker` calls with the **new continuation token** after each
  batch dispatches successfully, so you persist durable progress (typically to a lease container)
  and feed it back as `StartContinuation` on restart.

A batch whose dispatch is **unsuccessful** is not checkpointed and the token is not advanced, so the
same batch is re-read on the next poll and on restart — matching `CosmosHandler`'s "outer 500
redelivers the whole batch". The `OnFailure` hook makes that redelivery observable. Change-feed
delivery is **at-least-once**, so handlers must be idempotent.

## Running the worker

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
    benzene "github.com/daniellepelley/benzene-go"
    "github.com/daniellepelley/benzene-go/azurecosmos"
    "github.com/daniellepelley/benzene-go/wire"
)

func main() {
    // 1. Connect to Cosmos and get the container whose change feed you want to read.
    client, err := azcosmos.NewClientFromConnectionString(connString, nil)
    if err != nil {
        log.Fatal(err)
    }
    container, err := client.NewContainer("orders-db", "orders")
    if err != nil {
        log.Fatal(err)
    }

    // 2. Choose a feed range. Use container.ReadFeedRanges() to fan a large container across
    //    several workers (one range each), or the full range to read it all from one worker.
    ranges, err := container.ReadFeedRanges(context.Background(), nil)
    if err != nil {
        log.Fatal(err)
    }

    reader := azurecosmos.NewChangeFeedReader(container, ranges[0], nil /* startTime: nil = from the beginning */)

    // 3. Build your Benzene application (registry + pipeline) with a []Document handler on Topic.
    builder := buildApp()

    worker := &azurecosmos.Worker{
        Reader:            reader,
        Builder:           builder,
        Topic:             benzene.NewTopic("orders:changed"),
        StartContinuation: loadContinuation(), // "" on first run; else the last persisted token
        MaxBatchSize:      100,
        PollInterval:      5 * time.Second, // wait between polls while the feed is caught up
        OnFailure: func(ctx context.Context, page azurecosmos.ChangeFeedPage, resp wire.Response) {
            log.Printf("batch of %d documents failed: %s (will redeliver)", len(page.Documents), resp.StatusCode)
        },
        Checkpoint: func(ctx context.Context, continuation string) error {
            return saveContinuation(continuation) // persist to your lease container / store
        },
    }
    if err := worker.Validate(); err != nil {
        log.Fatal(err)
    }
    if err := worker.Run(context.Background()); err != nil {
        log.Printf("worker stopped: %v", err)
    }
}
```

`Run` polls until its context is cancelled, then returns `ctx.Err()`. A reader error, an
unsuccessful dispatch, or a checkpoint error backs off (`ErrorBackoff`, default 1s) and keeps
polling; a caught-up (empty) poll waits `PollInterval` (default 5s); a successful dispatch polls
again immediately to drain the rest of the feed.

> **Note:** `PollInterval` has no equivalent on the Event Hubs consumer. Unlike Event Hubs'
> blocking receive, a Cosmos change-feed read returns immediately when there are no changes, so this
> poll pacing is required to avoid hammering the container while idle.

## Field reference

| Field | Required | Default | Purpose |
| --- | --- | --- | --- |
| `Reader` | ✅ | — | The `ChangeFeedReader` each poll reads from. |
| `Builder` | ✅ | — | The application whose pipeline each batch dispatches through. |
| `Topic` | ✅ | — | The code-named topic every batch fans in to. |
| `StartContinuation` | | `""` | Continuation token to resume from (`""` = from the reader's beginning). |
| `MaxBatchSize` | | `100` | Max documents per poll (the change feed's `MaxItemCount` hint). |
| `PollInterval` | | `5s` | Wait after a caught-up (empty) poll. |
| `ErrorBackoff` | | `1s` | Wait after a reader error, failed dispatch, or checkpoint error. |
| `OnFailure` | | nil | Called for a batch whose dispatch was unsuccessful (batch redelivers). |
| `Checkpoint` | | nil | Called with the new continuation token after a successful batch (persist it). |

## What was and wasn't verified

This repo has **no live Azure Cosmos DB credentials**, so "verified" here means:

- **Verified:** the module builds and cross-compiles against the real `azcosmos` SDK API, and the
  `Worker`'s poll/dispatch/checkpoint/redelivery/backoff logic is covered by unit tests running
  against a **fake `ChangeFeedReader`** (`go test ./... -race -cover`). The core `Worker` is at
  100% statement coverage (bar a documented, unreachable defensive branch).
- **Not verified (live-only):** `NewChangeFeedReader` — the thin adapter that bridges a real
  `*azcosmos.ContainerClient.ReadChangeFeed` pager onto the `ChangeFeedReader` interface — is a
  pass-through to the SDK and is exercised only against a live Cosmos DB account. It carries no
  logic of its own beyond mapping the SDK's `ChangeFeedResponse` (`Items`, `ContinuationToken`)
  onto this package's `ChangeFeedPage`.

No lease-container schema, connection string, or deployment manifest is fabricated here — wiring the
durable checkpoint store to a real lease container is the application's responsibility, described
above by contract only.
