// Package gcpfunctions hosts a Benzene service as Google Cloud Functions (2nd gen)
// functions via the Functions Framework (https://cloud.google.com/functions/docs/functions-framework):
// the buildpack/functions-framework runtime serves your function, and this package registers
// a Benzene pipeline as the function it serves. It is the Go port of GoogleCloud.Functions.Http
// (the HTTP-triggered function) and GoogleCloud.Functions.PubSub (the CloudEvent-triggered
// function - Pub/Sub, Cloud Storage, Eventarc, and any other Eventarc source that delivers a
// CloudEvent).
//
// It needs two third-party dependencies and so lives in its own module: the Functions Framework
// itself (github.com/GoogleCloudPlatform/functions-framework-go), whose functions.HTTP /
// functions.CloudEvent are the declarative registration API a Gen2 Go function is built around,
// and the CloudEvents SDK (github.com/cloudevents/sdk-go/v2), whose event.Event is the concrete
// type the framework hands a CloudEvent function.
//
// Two entry points:
//
//   - RegisterHTTP registers an HTTP-triggered function serving a native httpbinding.Handler -
//     real HTTP status codes and an explicit route table, exactly as httpbinding does for a
//     self-hosted server.
//   - RegisterCloudEvent registers a CloudEvent-triggered function: each delivered CloudEvent is
//     mapped onto a Benzene wire.Request and dispatched through the pipeline. The mapping mirrors
//     the cloudevents package (type -> topic, data -> body, the other attributes -> "ce-"-prefixed
//     headers). A CloudEvent function acknowledges by returning nil and asks the platform to retry
//     by returning an error, so a dispatch whose Benzene result is unsuccessful becomes a returned
//     error - the same fire-and-forget, let-the-platform-retry posture as
//     azurefunctions.EventGridHandler's outer HTTP 500.
//
// The conversion-and-dispatch core is the unexported dispatchCloudEvent, kept separate from the
// framework registration so it is unit-testable against a hand-built event.Event with no live
// Functions Framework server and no live Google Cloud.
package gcpfunctions
