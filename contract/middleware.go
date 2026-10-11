package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// SpecRequest is the optional request of the benzene:spec topic (contract-document.md §7): which
// document, in which format. Empty fields mean type "benzene" and format "json" - the Contract
// Document as JSON. Values compare case-insensitively.
type SpecRequest struct {
	Type   string `json:"type,omitempty"`
	Format string `json:"format,omitempty"`
}

// DefaultType and DefaultFormat are what an absent request, or an absent field, means.
const (
	DefaultType   = "benzene"
	DefaultFormat = "json"
)

// MiddlewareOption configures Middleware.
type MiddlewareOption func(map[string]json.RawMessage)

// WithDocumentType serves payload for the (typ, format) pair - a port extension such as an AsyncAPI
// or OpenAPI document, which the spec permits and does not pin. payload is marshaled once, when the
// middleware is built; a payload that does not marshal panics there, at start-up.
//
//	contract.Middleware(doc, contract.WithDocumentType("asyncapi", "json", asyncapi.Generate(doc)))
func WithDocumentType(typ, format string, payload any) MiddlewareOption {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("contract: document type %s/%s does not marshal: %v", typ, format, err))
	}
	return func(served map[string]json.RawMessage) { served[selector(typ, format)] = raw }
}

func selector(typ, format string) string {
	return strings.ToLower(strings.TrimSpace(typ)) + "/" + strings.ToLower(strings.TrimSpace(format))
}

// Middleware answers the reserved benzene:spec topic (whatever its version) with doc, and passes
// every other topic on. It is pipeline middleware, so the topic runs behind whatever
// authentication the pipeline applies to every topic; install it ahead of RouterMiddleware.
// httpbinding.SpecHandler serves the same topic as GET /benzene/spec.
//
// The request is a SpecRequest: an absent body, an empty one, null and {} all ask for the
// Contract Document as JSON. A type or format this service does not serve is answered
// not-implemented, never with another document; a body that is not a SpecRequest is a
// bad-request.
//
// The document is marshaled once, here; serving it is a byte copy.
func Middleware(doc Document, opts ...MiddlewareOption) benzene.Middleware {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("contract: the document does not marshal: %v", err))
	}
	served := map[string]json.RawMessage{selector(DefaultType, DefaultFormat): raw}
	for _, opt := range opts {
		opt(served)
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		if ic.Topic.ID != TopicID {
			return next(ctx)
		}
		req, err := specRequest(ic.Request)
		if err != nil {
			ic.Result = benzene.BadRequest[any](err.Error())
			return nil
		}
		typ, format := req.Type, req.Format
		if strings.TrimSpace(typ) == "" {
			typ = DefaultType
		}
		if strings.TrimSpace(format) == "" {
			format = DefaultFormat
		}
		payload, ok := served[selector(typ, format)]
		if !ok {
			ic.Result = benzene.NotImplemented[any](fmt.Sprintf("benzene:spec does not serve type %q in format %q", typ, format))
			return nil
		}
		ic.Result = benzene.Ok(payload)
		return nil
	}
}

// specRequest reads the request off whatever the binding handed the pipeline: raw JSON bytes, or
// an already-typed SpecRequest.
func specRequest(raw any) (SpecRequest, error) {
	var data []byte
	switch v := raw.(type) {
	case nil:
		return SpecRequest{}, nil
	case SpecRequest:
		return v, nil
	case *SpecRequest:
		if v == nil {
			return SpecRequest{}, nil
		}
		return *v, nil
	case json.RawMessage:
		data = v
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return SpecRequest{}, fmt.Errorf("benzene:spec cannot read a request of type %T", raw)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return SpecRequest{}, nil
	}
	var req SpecRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return SpecRequest{}, fmt.Errorf("benzene:spec request must be {\"type\"?: string, \"format\"?: string}: %w", err)
	}
	return req, nil
}
