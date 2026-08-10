// Package azureeventhub is the Azure Event Hubs binding: an outbound producer Client that
// publishes wire messages to an Event Hub, and a self-hosted Consumer that receives events and
// dispatches each one through a Benzene pipeline. It is the Go port of the main repo's
// Benzene.Clients.Azure.EventHub (outbound) and Benzene.Azure.EventHub (self-hosted worker),
// applied to Azure Event Hubs via github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs.
//
// Event Hubs has no native concept of a message "topic", so - like the cloud queue bindings
// (awssqs/awssns/gcppubsub) - this binding carries the Benzene topic as an event property, matching
// Benzene.Clients.Azure.EventHub.EventHubContextConverter and
// Benzene.Azure.EventHub.EventHubConsumerMessageTopicGetter:
//
//   - Topic: an event property named by the reserved topic key (wire-contracts.md §2, default
//     "topic"; configurable via ReservedNames). The Consumer resolves it with
//     wire.ResolveMetadataTopic, falling back to parsing the body as a full wire envelope when no
//     topic property is present - the same two-tier resolution the awssqs Consumer uses.
//   - Headers: every other string-valued event property, verbatim (Event Hubs properties are
//     map[string]any; the topic getter and headers getter both read only string values).
//   - Body: the raw event data bytes, verbatim (EventData.Body on send, ReceivedEventData.Body on
//     receive) - no envelope wrapping.
//
// This module depends on the Azure Event Hubs SDK (an AMQP wire protocol is not hand-rollable),
// so it lives in its own Go module (see RELEASING.md) to keep the root module zero-dependency.
// Both halves depend only on narrow interfaces - ProducerAPI (outbound) and Receiver (inbound) -
// so this package's own tests run entirely against fakes, with no live Azure and without importing
// the SDK from the test files. Thin, documented adapters (NewProducerAdapter, NewPartitionReceiver)
// bridge the real *azeventhubs.ProducerClient / *azeventhubs.PartitionClient onto those interfaces.
//
// # Checkpointing is the caller's responsibility
//
// Event Hubs is a partitioned, checkpoint-based stream: at-least-once delivery is achieved by a
// consumer group persisting per-partition progress to a durable checkpoint store (typically an
// Azure Blob container, via azeventhubs' Processor / CheckpointStore), and partition ownership /
// load-balancing across worker instances is owned by that same machinery. That lifecycle is heavy
// and application-owned - so, exactly as azurefunctions defers the self-hosted Event Hub flavor,
// this Consumer deliberately does NOT implement a checkpoint store, partition leasing, or
// load-balancing. It is scoped to the receive-and-dispatch loop over the narrow Receiver interface:
// the caller supplies the receiver (wrapping a real PartitionClient / ProcessorPartitionClient) and
// an optional Checkpoint hook the Consumer calls after each event dispatches successfully, so
// durable progress is persisted by the caller's own store. A Consumer built without a Checkpoint
// hook simply re-reads from wherever its Receiver resumes; because delivery is at-least-once
// regardless, handlers must be idempotent.
package azureeventhub
