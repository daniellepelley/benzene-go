package grpcbinding

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/grpcstatus"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatuspkg "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ClientRoute maps one Benzene topic to a gRPC unary method - the outbound counterpart of
// Route, keyed by topic (matching the main repo's IGrpcClientRouteRegistry.Find(topic)).
// NewRequest/NewResponse construct fresh instances of the method's request/response message
// types, bridged to/from the raw JSON wire payload via protojson (see the package doc for why
// an explicit factory, not reflection).
type ClientRoute struct {
	Topic       benzene.Topic
	Method      string
	NewRequest  func() proto.Message
	NewResponse func() proto.Message
}

// Client publishes outbound Benzene messages as gRPC unary calls. It satisfies
// client.Sender, so it can be wrapped in client.WithCorrelationID/WithRetry like any
// other Sender.
type Client struct {
	Conn   grpc.ClientConnInterface
	routes map[string]ClientRoute
}

// NewClient returns a Client invoking unary calls over conn (typically a *grpc.ClientConn),
// resolving the target method per Send call from routes (keyed by topic).
func NewClient(conn grpc.ClientConnInterface, routes []ClientRoute) *Client {
	byTopic := make(map[string]ClientRoute, len(routes))
	for _, route := range routes {
		byTopic[route.Topic.String()] = route
	}
	return &Client{Conn: conn, routes: byTopic}
}

// Send invokes the gRPC method registered for topic (see ClientRoute), forwarding headers as
// outgoing metadata and message as the request body via protojson, and recovers the precise
// Benzene status: the "benzene-status" response trailer when the server set one
// (wire-contracts.md §4.2 - several Benzene statuses collapse onto one gRPC code, so the
// trailer is how a client recovers the exact one - a trailer, when present, wins verbatim),
// else grpcstatus.FromGRPC(code) as the coarse fallback - matching
// DefaultGrpcStatusReverseMapper exactly. A failed call's errors are recovered the same way: from
// the google.rpc.BadRequest field violations in the "grpc-status-details-bin" trailer when the
// server sent them (see recoverErrors), else the status message as a single error. A missing
// route maps to StatusNotImplemented
// (matching GrpcClientMiddleware's "No gRPC route has been registered for topic ..." ->
// StatusCode.Unimplemented, itself reverse-mapped to NotImplemented); a request that fails
// to marshal into the route's proto type, or a response that fails to marshal back to JSON,
// maps to ServiceUnavailable - "client-side send failures" per the status vocabulary's own
// description, matching every other Sender in this repo's transport-failure default.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	route, ok := c.routes[topic.String()]
	if !ok {
		return benzene.NotImplemented[json.RawMessage]("grpcbinding: no gRPC route registered for topic " + topic.String())
	}

	req := route.NewRequest()
	if err := protojson.Unmarshal(message, req); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("grpcbinding: failed to build request: " + err.Error())
	}

	outCtx := ctx
	if len(headers) > 0 {
		outCtx = metadata.NewOutgoingContext(ctx, metadata.New(headers))
	}

	var trailer metadata.MD
	resp := route.NewResponse()
	err := c.Conn.Invoke(outCtx, route.Method, req, resp, grpc.Trailer(&trailer))

	grpcResult := grpcstatuspkg.Convert(err) // never nil: err == nil -> Code() == codes.OK
	benzeneStatus := recoverStatus(int(grpcResult.Code()), trailer)

	if grpcResult.Code() != codes.OK {
		detail := grpcResult.Message()
		if detail == "" {
			detail = grpcResult.Code().String()
		}
		return benzene.Result[json.RawMessage]{Status: benzeneStatus, Errors: recoverErrors(grpcResult, detail)}
	}

	body, err := protojson.Marshal(resp)
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("grpcbinding: failed to read response: " + err.Error())
	}
	raw := json.RawMessage(body)
	return benzene.Result[json.RawMessage]{Status: benzeneStatus, Payload: &raw}
}

// recoverErrors rebuilds the failed result's structured errors from the gRPC error model
// (wire-contracts.md §4.2): one benzene.Error per google.rpc.BadRequest FieldViolation carried in
// the "grpc-status-details-bin" trailer, Description -> Message and Field -> Field, so the field a
// message belongs to survives the hop instead of being flattened into prose.
//
// A peer that attaches no such details - any non-Benzene gRPC server, or a Benzene one predating
// this - falls back to the single message-only error built from the status message, which is
// exactly what this client always did, so nothing regresses for a peer that doesn't send them.
func recoverErrors(status *grpcstatuspkg.Status, detail string) []benzene.Error {
	for _, item := range status.Details() {
		// Details() yields an error value for a detail it cannot unmarshal; anything that
		// isn't a BadRequest is simply not ours to read.
		badRequest, ok := item.(*errdetails.BadRequest)
		if !ok || len(badRequest.GetFieldViolations()) == 0 {
			continue
		}

		errors := make([]benzene.Error, 0, len(badRequest.GetFieldViolations()))
		for _, violation := range badRequest.GetFieldViolations() {
			errors = append(errors, benzene.Error{
				Message: violation.GetDescription(),
				Field:   violation.GetField(),
			})
		}
		return errors
	}

	return []benzene.Error{{Message: detail}}
}

// recoverStatus implements the trailer-wins-verbatim rule described on Send.
func recoverStatus(code int, trailer metadata.MD) benzene.Status {
	if values := trailer.Get(BenzeneStatusTrailer); len(values) > 0 && values[len(values)-1] != "" {
		return benzene.Status(values[len(values)-1])
	}
	return grpcstatus.FromGRPC(code)
}

var _ client.Sender = (*Client)(nil)
