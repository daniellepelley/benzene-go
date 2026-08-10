// Package azurecosmos is the self-hosted Azure Cosmos DB Change Feed binding: a Worker that
// opens the change feed itself and dispatches each delivered batch of changed documents through a
// Benzene pipeline. It is the Go port of the main repo's Benzene.Azure.CosmosDb (the self-hosted,
// non-Functions flavor), applied to Azure Cosmos DB via
// github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos.
//
// It is the standalone-compute counterpart of the zero-dependency azurefunctions.CosmosHandler.
// There, the Azure Functions host owns the change-feed connection and lease container and forwards
// each batch to a custom handler; here, the Worker owns the change-feed connection itself (a
// container, a Kubernetes deployment, a VM) and runs its own poll loop. The two differ only in who
// owns the feed and the durable position - not in how a batch is dispatched.
//
// # Fan-in, not topic-routed
//
// Like azurefunctions.CosmosHandler, this binding is fan-in, not topic-routed (core-concepts.md §3,
// streaming-shaped): the whole delivered batch of changed documents is one pipeline invocation - not
// one invocation per document - dispatched to the single topic the developer names in Worker.Topic,
// whose handler receives the batch as its request (idiomatically a slice, benzene.Handler[[]TDoc,
// TRes]). The change feed carries no per-document routing metadata - the changed documents are the
// payload - so there is no reserved topic key to resolve and no per-message header channel; the body
// is the JSON array of changed documents and the only synthesized header is cosmos-document-count,
// exactly as CosmosHandler produces. The dispatch itself goes through envelope.DispatchTopicResult,
// the same version-aware fan-in entry point CosmosHandler uses.
//
// # Checkpointing is the caller's responsibility
//
// The Cosmos change feed is resumed by a continuation token, and coordinating that token durably
// across worker instances is done through an application-owned lease container (the same machinery
// the Change Feed Processor and the Functions trigger rely on). That lifecycle is heavy and
// application-owned - so, exactly as the sibling azureeventhub.Consumer defers Event Hubs' durable
// checkpoint store, this Worker deliberately does NOT implement a lease container, partition
// ownership, or load-balancing. It is scoped to the poll-and-dispatch loop over the narrow
// ChangeFeedReader interface: the caller supplies the reader (wrapping a real
// *azcosmos.ContainerClient), an optional StartContinuation to resume from, and an optional
// Checkpoint hook the Worker calls with the new continuation token after each batch dispatches
// successfully, so durable progress is persisted by the caller's own store. A Worker built without a
// Checkpoint hook simply re-reads from wherever its reader resumes; because change feed delivery is
// at-least-once regardless (a redelivered or replayed batch is always possible), handlers must be
// idempotent.
//
// # Redelivery on failure
//
// A batch whose dispatch is unsuccessful is NOT checkpointed and the continuation token is NOT
// advanced, so the same batch is re-read on the next poll and on restart - matching CosmosHandler's
// "outer 500 redelivers the whole batch". This is stop-at-batch-failure: the durable position never
// moves past unhandled work, and the OnFailure hook makes the redelivery observable rather than
// silent (the same no-silent-drop rule the awss3/awssns bindings follow).
//
// This module depends on the Azure Cosmos DB SDK, so it lives in its own Go module (see RELEASING.md)
// to keep the root module zero-dependency. The Worker depends only on the narrow ChangeFeedReader
// interface, so this package's own tests run entirely against a fake, with no live Cosmos and without
// importing the SDK from the test files. A thin, documented adapter (NewChangeFeedReader) bridges a
// real *azcosmos.ContainerClient's ReadChangeFeed pager onto that interface.
package azurecosmos
