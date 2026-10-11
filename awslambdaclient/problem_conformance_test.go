package awslambdaclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/validation"
)

// core-concepts.md §12, error fidelity (Core): the client rebuilds a failure's errors from the
// problem document in the response envelope with field and code intact. The vendored
// problem-details-cases.json's envelopeCases run end to end: Client.Send invokes (through a fake
// InvokeAPI) a target built with awslambda.EnvelopeHandler over the canonical handlers they target,
// and the Result the caller gets back is held to the case's status and errors. The root module's
// conformance runner does the same for httpclient and inprocess.

// problemInvokeAPI is a target Lambda: it runs the invoke payload through awslambda.EnvelopeHandler.
type problemInvokeAPI struct{ target awslambda.HandlerFunc }

func (f problemInvokeAPI) Invoke(ctx context.Context, params *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	payload, err := f.target(ctx, params.Payload)
	if err != nil {
		return nil, err
	}
	return &lambda.InvokeOutput{StatusCode: 200, Payload: payload}, nil
}

// The canonical handlers the envelopeCases target (conformance/README.md "Canonical handlers"):
// conformance:problem answers validation-error with one error built from the request (or the
// application's own problem type when appType is set); conformance:validate validates through
// validation.Validated, naming each offending value with a JSON Pointer.
type problemRequest struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code,omitempty"`
	AppType string `json:"appType,omitempty"`
}

func problemHandler(_ context.Context, req problemRequest) benzene.Result[struct{}] {
	problemError := benzene.Error{Message: req.Message, Field: req.Field, Code: req.Code}
	if req.AppType != "" {
		return benzene.ProblemResult[struct{}](benzene.Problem{
			Type:          req.AppType,
			BenzeneStatus: string(benzene.StatusValidationError),
			Errors:        []benzene.Error{problemError},
		})
	}
	return benzene.ValidationErrorWith[struct{}](problemError)
}

type validateRequest struct {
	Name  string `json:"name"`
	Items []struct {
		SKU string `json:"sku"`
	} `json:"items"`
}

var validateHandler = validation.Validated(
	validation.ValidatorFunc[validateRequest](func(req validateRequest) []benzene.Error {
		var problems []benzene.Error
		if req.Name == "" {
			problems = append(problems, benzene.Error{Message: "name must not be empty", Field: validation.Pointer("name"), Code: "required"})
		}
		for i, item := range req.Items {
			if item.SKU == "" {
				problems = append(problems, benzene.Error{Message: "sku must not be empty", Field: validation.Pointer("items", i, "sku"), Code: "required"})
			}
		}
		return problems
	}),
	func(context.Context, validateRequest) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) },
)

type problemCase struct {
	Name     string `json:"name"`
	Requires string `json:"requires"`
	Request  struct {
		Topic   string            `json:"topic"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Expected struct {
		StatusCode string `json:"statusCode"`
		Body       struct {
			Errors []benzene.Error `json:"errors"`
		} `json:"body"`
		ErrorFields []string `json:"errorFields"`
	} `json:"expected"`
}

func TestConformance_ProblemClientErrorFidelity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "conformance", "testdata", "problem-details-cases.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		EnvelopeCases []problemCase `json:"envelopeCases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fixture.EnvelopeCases) == 0 {
		t.Fatal("problem-details-cases: envelopeCases is empty - the runner and the fixture have drifted")
	}

	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("conformance:problem"), benzene.Handler[problemRequest, struct{}](problemHandler))
	benzene.MustRegister(registry, benzene.NewTopic("conformance:validate"), validateHandler)
	builder := (&benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer()}).UseDefaultPipeline()
	client := NewClient(problemInvokeAPI{target: awslambda.EnvelopeHandler(builder)}, "conformance")

	for _, c := range fixture.EnvelopeCases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Requires != "" && c.Requires != "validation" {
				t.Skipf("requires %q, which this port does not implement", c.Requires)
			}
			result := client.Send(context.Background(), benzene.NewTopic(c.Request.Topic), c.Request.Headers, []byte(c.Request.Body))
			if string(result.Status) != c.Expected.StatusCode {
				t.Errorf("status = %q, want %q", result.Status, c.Expected.StatusCode)
			}
			if want := c.Expected.Body.Errors; want != nil {
				if len(result.Errors) != len(want) {
					t.Fatalf("errors = %+v, want %+v", result.Errors, want)
				}
				for i := range want {
					if result.Errors[i] != want[i] {
						t.Errorf("errors[%d] = %+v, want %+v (message, field and code intact)", i, result.Errors[i], want[i])
					}
				}
			}
			if c.Expected.ErrorFields != nil {
				var fields []string
				for _, e := range result.Errors {
					fields = append(fields, e.Field)
				}
				want := append([]string(nil), c.Expected.ErrorFields...)
				sort.Strings(fields)
				sort.Strings(want)
				if strings.Join(fields, ",") != strings.Join(want, ",") {
					t.Errorf("errors[].field = %v, want %v", fields, want)
				}
			}
		})
	}
}
