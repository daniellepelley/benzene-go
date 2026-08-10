package responseevents

import (
	"reflect"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type orderDoc struct {
	ID string `json:"id"`
}

// fakeResult is a ResultInfo that does NOT implement the optional ResultIsSuccessful interface, so
// it exercises resultSuccessful's status-class fallback path.
type fakeResult struct {
	status  benzene.Status
	payload any
}

func (r fakeResult) ResultStatus() benzene.Status { return r.status }
func (r fakeResult) ResultErrors() []string       { return nil }
func (r fakeResult) ResultPayload() any           { return r.payload }

func TestExplicitMapping_Resolve(t *testing.T) {
	order := orderDoc{ID: "o-1"}
	tests := []struct {
		name    string
		mapping Mapping
		topic   benzene.Topic
		result  benzene.ResultInfo
		want    *Publication
	}{
		{
			name:    "matches source and publishes on success with payload",
			mapping: Map("order:create", "order:created"),
			topic:   benzene.NewTopic("order:create"),
			result:  benzene.Created(order),
			want:    &Publication{EventTopic: benzene.NewTopic("order:created"), Payload: order},
		},
		{
			name:    "source match is case-insensitive",
			mapping: Map("Order:Create", "order:created"),
			topic:   benzene.NewTopic("order:create"),
			result:  benzene.Created(order),
			want:    &Publication{EventTopic: benzene.NewTopic("order:created"), Payload: order},
		},
		{
			name:    "different source topic does not fire",
			mapping: Map("order:create", "order:created"),
			topic:   benzene.NewTopic("order:update"),
			result:  benzene.Created(order),
			want:    nil,
		},
		{
			name:    "unsuccessful result does not fire (default predicate)",
			mapping: Map("order:create", "order:created"),
			topic:   benzene.NewTopic("order:create"),
			result:  benzene.BadRequest[orderDoc]("nope"),
			want:    nil,
		},
		{
			name:    "successful but nil payload does not fire",
			mapping: Map("order:get", "order:read"),
			topic:   benzene.NewTopic("order:get"),
			result:  benzene.Ignored[any](nil),
			want:    nil,
		},
		{
			name:    "nil result is defensively treated as not firing",
			mapping: Map("order:create", "order:created"),
			topic:   benzene.NewTopic("order:create"),
			result:  nil,
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.mapping.Resolve(tc.topic, tc.result)
			assertPublication(t, got, tc.want)
		})
	}
}

func TestExplicitMapping_WhenPredicateOverridesStatus(t *testing.T) {
	// A When predicate replaces the success check: here it fires on a specific failure status, but the
	// non-nil-payload requirement still applies.
	mapping := Map("order:create", "order:rejected", When(func(r benzene.ResultInfo) bool {
		return r.ResultStatus() == benzene.StatusBadRequest
	}))

	fired := mapping.Resolve(benzene.NewTopic("order:create"), benzene.BadRequest[orderDoc]("bad"))
	if fired != nil {
		t.Errorf("Resolve() = %+v, want nil - BadRequest carries no payload so there is nothing to publish", fired)
	}

	// With a payload-carrying result the predicate lets it through.
	withPayload := fakeResult{status: benzene.StatusBadRequest, payload: orderDoc{ID: "o-9"}}
	got := mapping.Resolve(benzene.NewTopic("order:create"), withPayload)
	assertPublication(t, got, &Publication{EventTopic: benzene.NewTopic("order:rejected"), Payload: orderDoc{ID: "o-9"}})
}

func TestExplicitMapping_Project(t *testing.T) {
	t.Run("reshapes payload", func(t *testing.T) {
		mapping := Map("order:create", "order:created", Project(func(p any) any {
			return orderDoc{ID: p.(orderDoc).ID + "-projected"}
		}))
		got := mapping.Resolve(benzene.NewTopic("order:create"), benzene.Created(orderDoc{ID: "o-1"}))
		assertPublication(t, got, &Publication{EventTopic: benzene.NewTopic("order:created"), Payload: orderDoc{ID: "o-1-projected"}})
	})

	t.Run("nil projection skips the publish", func(t *testing.T) {
		mapping := Map("order:create", "order:created", Project(func(any) any { return nil }))
		if got := mapping.Resolve(benzene.NewTopic("order:create"), benzene.Created(orderDoc{ID: "o-1"})); got != nil {
			t.Errorf("Resolve() = %+v, want nil when the projection returns nil", got)
		}
	})
}

func TestExplicitMapping_CoversAndDescription(t *testing.T) {
	plain := Map("order:create", "order:created")
	if !plain.Covers(benzene.NewTopic("ORDER:CREATE")) {
		t.Error("Covers() = false, want true (case-insensitive source match)")
	}
	if plain.Covers(benzene.NewTopic("order:update")) {
		t.Error("Covers() = true for an unrelated topic, want false")
	}
	if got, want := plain.Description(), "order:create -> order:created"; got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}

	conditional := Map("order:create", "order:created", When(func(benzene.ResultInfo) bool { return true }))
	if got, want := conditional.Description(), "order:create -> order:created [conditional]"; got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestCrudConvention_Resolve(t *testing.T) {
	order := orderDoc{ID: "o-1"}
	tests := []struct {
		name   string
		topic  string
		result benzene.ResultInfo
		want   *Publication
	}{
		{"create with Created publishes created", "order:create", benzene.Created(order), &Publication{EventTopic: benzene.NewTopic("order:created"), Payload: order}},
		{"update with Updated publishes updated", "order:update", benzene.Updated(order), &Publication{EventTopic: benzene.NewTopic("order:updated"), Payload: order}},
		{"delete with Deleted publishes deleted", "order:delete", benzene.Deleted(order), &Publication{EventTopic: benzene.NewTopic("order:deleted"), Payload: order}},
		{"verb match is case-insensitive", "order:Create", benzene.Created(order), &Publication{EventTopic: benzene.NewTopic("order:Created"), Payload: order}},
		{"create with wrong status does not fire", "order:create", benzene.Ok(order), nil},
		{"non-crud verb does not fire", "order:get", benzene.Ok(order), nil},
		{"crud verb but nil payload does not fire", "order:create", benzene.Created[any](nil), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CrudConvention().Resolve(benzene.NewTopic(tc.topic), tc.result)
			assertPublication(t, got, tc.want)
		})
	}
}

func TestCrudConvention_CoversAndVerbParsing(t *testing.T) {
	crud := CrudConvention()
	if !crud.Covers(benzene.NewTopic("order:create")) {
		t.Error("Covers(order:create) = false, want true")
	}
	if crud.Covers(benzene.NewTopic("order:get")) {
		t.Error("Covers(order:get) = true, want false")
	}
	// An unsegmented topic falls back to the whole id as the verb.
	if !crud.Covers(benzene.NewTopic("create")) {
		t.Error("Covers(create) = false, want true (unsegmented id is the verb)")
	}
	if crud.Description() == "" {
		t.Error("Description() is empty")
	}
}

func assertPublication(t *testing.T, got, want *Publication) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("Resolve() = %+v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("Resolve() = nil, want %+v", want)
	}
	if got.EventTopic != want.EventTopic {
		t.Errorf("EventTopic = %v, want %v", got.EventTopic, want.EventTopic)
	}
	if !reflect.DeepEqual(got.Payload, want.Payload) {
		t.Errorf("Payload = %+v, want %+v", got.Payload, want.Payload)
	}
}
