package azurecosmos

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// changeFeedReader bridges a real *azcosmos.ContainerClient's ReadChangeFeed pager onto the narrow
// ChangeFeedReader interface the Worker depends on.
type changeFeedReader struct {
	container *azcosmos.ContainerClient
	feedRange azcosmos.FeedRange
	startTime *time.Time
}

// NewChangeFeedReader wraps a real *azcosmos.ContainerClient so a Worker can read its change feed.
// feedRange is the range of partition-key values to read - use a range from container.ReadFeedRanges
// (one worker per range fans the whole container out across instances) or the full range to read the
// whole container from one worker. startTime, when non-nil, starts a fresh read (empty continuation)
// from that instant instead of from the beginning of the feed; a resumed read (a non-empty
// continuation, e.g. one a Worker.Checkpoint hook persisted) ignores it and continues from the token.
//
// The caller owns the container client's connection and lifetime, and owns durable checkpointing: the
// Worker hands each successful batch's continuation token to its Checkpoint hook, and the caller
// persists it (typically to a lease container) and feeds it back as Worker.StartContinuation on
// restart. This is a thin pass-through to the real SDK; this package's tests exercise the Worker
// against a ChangeFeedReader fake instead, so this adapter is covered only by a live Cosmos DB.
func NewChangeFeedReader(container *azcosmos.ContainerClient, feedRange azcosmos.FeedRange, startTime *time.Time) ChangeFeedReader {
	return changeFeedReader{container: container, feedRange: feedRange, startTime: startTime}
}

func (r changeFeedReader) ReadNext(ctx context.Context, continuation string, maxItems int) (ChangeFeedPage, error) {
	options := &azcosmos.ChangeFeedOptions{}
	if maxItems > 0 {
		options.MaxItemCount = int32(maxItems)
	}
	if continuation != "" {
		// Resume: the composite continuation token drives the read; the feed range and start time it
		// was first issued for are carried inside the token.
		options.Continuation = &continuation
	} else {
		// Fresh start: the read is anchored to the feed range, optionally from a start time.
		fr := r.feedRange
		options.FeedRange = &fr
		options.StartFrom = r.startTime
	}

	resp, err := r.container.ReadChangeFeed(ctx, options)
	if err != nil {
		return ChangeFeedPage{}, err
	}

	documents := make([]json.RawMessage, len(resp.Items))
	for i, item := range resp.Items {
		documents[i] = json.RawMessage(item)
	}
	return ChangeFeedPage{Documents: documents, Continuation: resp.ContinuationToken}, nil
}

// Compile-time assertions.
var (
	_ ChangeFeedReader = changeFeedReader{}
	_ ChangeFeedReader = (*changeFeedReader)(nil)
)
