package awsdynamodb

import (
	"encoding/json"
	"reflect"
	"testing"
)

// asAny parses JSON into a comparable interface tree (numbers become float64), so tests assert on
// value equality regardless of key order or formatting.
func asAny(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return v
}

func TestAttributeMapToJSON(t *testing.T) {
	tests := []struct {
		name       string
		attributes string
		want       string
	}{
		{"string", `{"Msg":{"S":"hi"}}`, `{"Msg":"hi"}`},
		{"number int", `{"Id":{"N":"101"}}`, `{"Id":101}`},
		{"number negative decimal", `{"Bal":{"N":"-12.5"}}`, `{"Bal":-12.5}`},
		{"number exponent", `{"Big":{"N":"1.5e3"}}`, `{"Big":1.5e3}`},
		{"bool", `{"Active":{"BOOL":true}}`, `{"Active":true}`},
		{"null", `{"Gone":{"NULL":true}}`, `{"Gone":null}`},
		{"binary scalar stays base64 string", `{"Blob":{"B":"YmFzZTY0"}}`, `{"Blob":"YmFzZTY0"}`},
		{"nested map", `{"Item":{"M":{"K":{"S":"v"},"N":{"N":"7"}}}}`, `{"Item":{"K":"v","N":7}}`},
		{"list of mixed", `{"Xs":{"L":[{"S":"a"},{"N":"1"},{"BOOL":false}]}}`, `{"Xs":["a",1,false]}`},
		{"string set", `{"Tags":{"SS":["a","b"]}}`, `{"Tags":["a","b"]}`},
		{"number set", `{"Nums":{"NS":["1","2","3"]}}`, `{"Nums":[1,2,3]}`},
		{"binary set stays base64 strings", `{"Bs":{"BS":["YQ==","Yg=="]}}`, `{"Bs":["YQ==","Yg=="]}`},
		{"deeply nested", `{"O":{"M":{"L":{"L":[{"M":{"K":{"N":"9"}}}]}}}}`, `{"O":{"L":[{"K":9}]}}`},
		{"empty map", `{}`, `{}`},
		{"empty attribute value is null", `{"X":{}}`, `{"X":null}`},
		{"unknown descriptor passes raw through", `{"X":{"WEIRD":123}}`, `{"X":123}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := attributeMapToJSON(json.RawMessage(tt.attributes))
			if err != nil {
				t.Fatalf("attributeMapToJSON() error = %v", err)
			}
			if !reflect.DeepEqual(asAny(t, got), asAny(t, []byte(tt.want))) {
				t.Errorf("attributeMapToJSON(%s) = %s, want %s", tt.attributes, got, tt.want)
			}
		})
	}
}

func TestAttributeMapToJSON_Errors(t *testing.T) {
	tests := []struct {
		name       string
		attributes string
	}{
		{"map is not an object", `["not","a","map"]`},
		{"attribute value is not an object", `{"X":"raw"}`},
		{"invalid number", `{"N":{"N":"not-a-number"}}`},
		{"number value is not a string", `{"N":{"N":123}}`},
		{"invalid number in set", `{"Ns":{"NS":["1","bad"]}}`},
		{"number set is not an array", `{"Ns":{"NS":"not-an-array"}}`},
		{"malformed list", `{"L":{"L":"not-an-array"}}`},
		{"list element error propagates", `{"L":{"L":[{"N":"bad"}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := attributeMapToJSON(json.RawMessage(tt.attributes)); err == nil {
				t.Errorf("attributeMapToJSON(%s) = nil error, want an error", tt.attributes)
			}
		})
	}
}

func TestIsJSONObject(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:  true,
		` {"a":1}`: true,
		``:         false,
		`null`:     false,
		`[1,2]`:    false,
		`"str"`:    false,
	}
	for raw, want := range cases {
		if got := isJSONObject(json.RawMessage(raw)); got != want {
			t.Errorf("isJSONObject(%q) = %v, want %v", raw, got, want)
		}
	}
}
