// Package grpcbinding is the gRPC binding
// (docs/specification/transport-bindings.md's "gRPC — Benzene.Grpc (+ .AspNet)" entry in the
// main repo): a server-side interceptor that claims specific registered gRPC methods for
// Benzene dispatch, plus an outbound client. It needs google.golang.org/grpc (Go has no gRPC
// support in the standard library) and google.golang.org/protobuf for the proto3-JSON
// bridging the spec calls for, so it lives in its own Go module (see RELEASING.md).
//
// Unlike this repo's other bindings, grpcbinding does not own the server: per the spec,
// "Unmatched methods fall through to the native generated service — the binding claims
// routes, it doesn't own the server." A UnaryServerInterceptor wraps an ordinary
// protoc-generated gRPC service exactly as any other interceptor would
// (grpc.NewServer(grpc.UnaryInterceptor(...)), or grpc.ChainUnaryInterceptor alongside
// others); the app still writes and registers real generated service code, matching this
// repo's explicit-registration stance (porting-guide.md: no attribute/reflection-based
// codegen scanning) - Route is the explicit registration, the same shape as
// httpbinding.Route or awslambda's route table, just keyed by gRPC's own routing key (the
// full method path) instead of an HTTP (verb, path) pair.
//
// The mapping, matching the spec exactly:
//
//   - Topic: the full method path (e.g. "/greet.GreetService/Greet"), matched
//     case-insensitively against the registered Route table.
//   - Headers: incoming metadata, both directions (binary "-bin" keys are skipped - they
//     have no flat-string representation in the wire header dictionary).
//   - Body: proto3-JSON bridging. The inbound request is already the concrete generated
//     type (grpc-go's own codec decoded it before the interceptor ever sees it), so it's
//     marshaled to JSON directly; the outbound response is unmarshaled from the dispatch
//     result's JSON body into a fresh instance from the route's NewResponse factory (Go has
//     no runtime type parameter to construct an arbitrary registered message from, unlike
//     .NET generics - an explicit factory is this port's non-reflective equivalent, kept off
//     the dispatch path per mesh's own reflect policy).
//   - Status: wire-contracts.md §4.2 via the grpcstatus package. The "benzene-status"
//     trailer is set unconditionally, success and failure alike, since several Benzene
//     statuses collapse onto one gRPC code; a non-OK result becomes a status.Error carrying
//     the joined error messages (or the bare status string if there are none) as its detail
//   - matching GrpcMethodHandler.RunPipelineAsync exactly, including throwing before any
//     response is returned (ordinary unary gRPC has no room for both a response and an
//     error).
//   - Cancellation: a context cancelled or past its deadline by the time dispatch returns
//     maps to Cancelled/DeadlineExceeded directly, bypassing the Benzene status mapping -
//     the same distinction GrpcMethodHandler.RunPipelineAsync's OperationCanceledException
//     catch draws.
//
// Scope: unary RPCs only. The main repo's binding also covers client-streaming,
// server-streaming, and duplex-streaming shapes (core-concepts.md §3's stream-of-items
// model) - a materially larger design surface (multiplexed message streams, backpressure,
// partial-failure-mid-stream semantics) deliberately left as a documented gap for a later,
// separate addition rather than a first cut bolted onto this one.
package grpcbinding

import (
	"context"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/grpcstatus"
	"github.com/daniellepelley/benzene-go/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatuspkg "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// BenzeneStatusTrailer is the reserved trailer key a Benzene gRPC server always sets,
// carrying the raw Benzene status string verbatim (wire-contracts.md §4.2) - several Benzene
// statuses collapse onto one gRPC code, so a client that wants the precise status (rather
// than the coarser gRPC code) reads this trailer.
const BenzeneStatusTrailer = "benzene-status"

// Route maps one full gRPC method path to a topic - the routing rule transport-bindings.md's
// gRPC entry calls for. Method is matched case-insensitively against the incoming call's
// info.FullMethod (e.g. "/greet.GreetService/Greet", generated-code's own constant for the
// method). NewResponse constructs a fresh, empty instance of the method's response message
// type - Send's dispatch result is unmarshaled into it via protojson (see the package doc for
// why an explicit factory, not reflection).
type Route struct {
	Method      string
	Topic       benzene.Topic
	NewResponse func() proto.Message
}

// UnaryServerInterceptor returns a grpc.UnaryServerInterceptor claiming the unary methods
// named in routes (case-insensitive full-method match) for Benzene dispatch; every other
// method falls through to handler unchanged - the native generated service still serves it,
// untouched. Register it the same way as any other interceptor:
// grpc.NewServer(grpc.UnaryInterceptor(grpcbinding.UnaryServerInterceptor(builder, routes))).
func UnaryServerInterceptor(builder *benzene.ApplicationBuilder, routes []Route) grpc.UnaryServerInterceptor {
	byMethod := make(map[string]Route, len(routes))
	for _, route := range routes {
		byMethod[strings.ToLower(route.Method)] = route
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		route, ok := byMethod[strings.ToLower(info.FullMethod)]
		if !ok {
			return handler(ctx, req)
		}

		message, ok := req.(proto.Message)
		if !ok {
			return nil, grpcstatuspkg.Error(codes.Internal, "grpcbinding: request does not implement proto.Message")
		}
		body, err := protojson.Marshal(message)
		if err != nil {
			return nil, grpcstatuspkg.Error(codes.Internal, "grpcbinding: failed to marshal request: "+err.Error())
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, wire.Request{
			Topic:   route.Topic.String(),
			Headers: incomingHeaders(ctx),
			Body:    string(body),
		})

		if cancelErr := cancellationError(ctx); cancelErr != nil {
			return nil, cancelErr
		}

		grpc.SetTrailer(ctx, metadata.Pairs(BenzeneStatusTrailer, trailerValue(resp.StatusCode)))

		code := codes.Code(grpcstatus.ToGRPC(benzene.Status(resp.StatusCode)))
		if code != codes.OK {
			return nil, grpcstatuspkg.Error(code, errorDetail(resp))
		}

		if len(resp.Headers) > 0 {
			if err := grpc.SetHeader(ctx, metadata.New(resp.Headers)); err != nil {
				return nil, grpcstatuspkg.Error(codes.Internal, "grpcbinding: failed to write response headers: "+err.Error())
			}
		}

		response := route.NewResponse()
		if err := protojson.Unmarshal([]byte(resp.Body), response); err != nil {
			return nil, grpcstatuspkg.Error(codes.Internal, "grpcbinding: failed to unmarshal response: "+err.Error())
		}
		return response, nil
	}
}

// trailerValue reports "Unknown" for an empty status - a dispatch that somehow produced no
// status at all - matching GrpcMethodHandler.RunPipelineAsync's
// `status ?? "Unknown"` exactly.
func trailerValue(status string) string {
	if status == "" {
		return "Unknown"
	}
	return status
}

// errorDetail is the gRPC status message for a non-OK result: the joined error messages, or
// the bare status string when there are none - matching
// GrpcMethodHandler.RunPipelineAsync's `errors is {Length: > 0} ? string.Join("; ", errors) :
// status ?? "Error"` exactly.
func errorDetail(resp wire.Response) string {
	if payload, err := wire.UnmarshalErrorPayload([]byte(resp.Body)); err == nil && payload.Detail != "" {
		return payload.Detail
	}
	if resp.StatusCode != "" {
		return resp.StatusCode
	}
	return "Error"
}

// cancellationError reports the gRPC status a cancelled or deadline-exceeded context maps
// to, bypassing the ordinary Benzene status mapping entirely (see the package doc) - nil if
// ctx carries no such error.
func cancellationError(ctx context.Context) error {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return grpcstatuspkg.Error(codes.DeadlineExceeded, "the call was cancelled")
	case context.Canceled:
		return grpcstatuspkg.Error(codes.Canceled, "the call was cancelled")
	default:
		return nil
	}
}

// incomingHeaders flattens gRPC's incoming metadata (multi-valued, comma-joined by
// google.golang.org/grpc/metadata itself for non-binary keys) into wire-contracts.md §2's
// flat map, skipping binary ("-bin"-suffixed) keys - they have no flat-string
// representation, matching GrpcMessageHeadersGetter's IsBinary skip exactly.
func incomingHeaders(ctx context.Context) map[string]string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return map[string]string{}
	}
	headers := make(map[string]string, len(md))
	for key, values := range md {
		if strings.HasSuffix(key, "-bin") || len(values) == 0 {
			continue
		}
		headers[key] = values[len(values)-1]
	}
	return headers
}
