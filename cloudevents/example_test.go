package cloudevents_test

import (
	"encoding/json"
	"fmt"

	"github.com/daniellepelley/benzene-go/cloudevents"
)

// ExampleToRequest shows the CloudEvents 1.0 mapping onto Benzene's transport-neutral wire envelope:
// the event's `type` becomes the topic, its `data` becomes the request body, and the other attributes
// (id, source, subject, ...) ride along as `ce-`-prefixed headers. This is how a CloudEvents-shaped
// delivery (Pub/Sub, Event Grid, an inbound HTTP CloudEvent) is turned into a message the pipeline
// dispatches.
func ExampleToRequest() {
	event := cloudevents.Event{
		SpecVersion: "1.0",
		ID:          "evt-1",
		Type:        "order.created",
		Source:      "/orders",
		Data:        json.RawMessage(`{"id":"o-1"}`),
	}

	req := cloudevents.ToRequest(event)

	fmt.Println("topic:", req.Topic)
	fmt.Println("body: ", string(req.Body))
	fmt.Println("source header:", req.Headers["ce-source"])
	// Output:
	// topic: order.created
	// body:  {"id":"o-1"}
	// source header: /orders
}
