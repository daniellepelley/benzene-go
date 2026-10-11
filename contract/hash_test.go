package contract

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func nanValue() float64 { return math.NaN() }

func project(t *testing.T, doc string, withSends bool) string {
	t.Helper()
	generic, err := decodeGeneric([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return canon(t, Projection(generic, withSends))
}

func TestProjection_MembersAndEntries(t *testing.T) {
	doc := `{
		"service": "s", "contractHash": "x", "transports": ["http"],
		"requests": [
			{"topic": "b", "version": "v1", "request": {}, "example": 1, "reserved": true},
			{"topic": "b", "request": {}, "httpMappings": [{"method": "post", "path": "/b", "extra": 1}, {"method": "GET", "path": "/b"}, {"method": "GET", "path": "/a"}, "junk"]},
			{"topic": "a", "version": "", "request": {}},
			{"topic": "benzene:spec", "request": {}},
			"junk"
		],
		"events": [{"topic": "e", "message": {}, "example": {}}],
		"sends": [{"topic": "s", "request": {}, "response": {}}],
		"components": {"schemas": {}}
	}`
	want := `{"events":[{"message":{},"topic":"e"}],"requests":[{"request":{},"topic":"a","version":""},` +
		`{"httpMappings":[{"method":"GET","path":"/a"},{"method":"GET","path":"/b"},{"method":"POST","path":"/b"}],"request":{},"topic":"b"},` +
		`{"request":{},"topic":"b","version":"v1"}]}`
	if got := project(t, doc, false); got != want {
		t.Errorf("contract projection:\n got  %s\n want %s", got, want)
	}
	if got := project(t, doc, true); !strings.Contains(got, `"sends":[{"request":{},"response":{},"topic":"s"}]`) {
		t.Errorf("registration projection lacks sends: %s", got)
	}
	if got := project(t, `{}`, true); got != `{"events":[],"requests":[],"sends":[]}` {
		t.Errorf("empty document projection = %s", got)
	}
	if got := project(t, `{"requests":[{"topic":"a","request":{},"httpMappings":"bad"}]}`, false); !strings.Contains(got, `"httpMappings":"bad"`) {
		t.Errorf("a malformed httpMappings is kept as written: %s", got)
	}
}

func TestProjection_SchemaNormalization(t *testing.T) {
	cases := map[string]struct{ schema, components, want string }{
		"annotations removed, required sorted": {
			`{"type":"object","title":"T","x-y":1,"format":"f","description":"d","required":["b","a"],"properties":{"a":{"type":"string","example":"e"},"b":{}}}`,
			`{}`,
			`{"properties":{"a":{"type":"string"},"b":{}},"required":["a","b"],"type":"object"}`,
		},
		"empty required and properties removed": {
			`{"type":"object","required":[],"properties":{}}`, `{}`, `{"type":"object"}`,
		},
		"non-schema keyword values kept as written": {
			`{"enum":[{"title":"kept"}],"const":{"$ref":"#/components/schemas/A"},"required":[1],"properties":"odd","allOf":"odd"}`,
			`{"A":{"type":"string"}}`,
			`{"allOf":"odd","const":{"$ref":"#/components/schemas/A"},"enum":[{"title":"kept"}],"properties":"odd","required":[1]}`,
		},
		"refs dereferenced at every position, siblings ignored": {
			`{"items":{"$ref":"#/components/schemas/A","description":"x"},"additionalProperties":{"$ref":"#/components/schemas/A"},"not":{"$ref":"#/components/schemas/A"},"anyOf":[{"$ref":"#/components/schemas/A"},true],"oneOf":[{"$ref":"#/components/schemas/A"}]}`,
			`{"A":{"type":"string","title":"A"}}`,
			`{"additionalProperties":{"type":"string"},"anyOf":[{"type":"string"},true],"items":{"type":"string"},"not":{"type":"string"},"oneOf":[{"type":"string"}]}`,
		},
		"additionalProperties boolean kept": {`{"additionalProperties":false}`, `{}`, `{"additionalProperties":false}`},
		"unknown and foreign refs are {}": {
			`{"properties":{"a":{"$ref":"#/components/schemas/Missing"},"b":{"$ref":"other.json#/X"},"c":{"$ref":5}}}`,
			`{}`,
			`{"properties":{"a":{},"b":{},"c":{}}}`,
		},
		"discriminator mapping removed": {
			`{"oneOf":[],"discriminator":{"propertyName":"kind","mapping":{"a":"#/components/schemas/A"}}}`,
			`{}`,
			`{"discriminator":{"propertyName":"kind"},"oneOf":[]}`,
		},
		"malformed discriminator kept": {`{"discriminator":"x"}`, `{}`, `{"discriminator":"x"}`},
		"cycle cut and nullable wrapper collapsed": {
			`{"$ref":"#/components/schemas/Node"}`,
			`{"Node":{"type":"object","properties":{"parent":{"type":"object","nullable":true,"description":"d","allOf":[{"$ref":"#/components/schemas/Node"}]},"kids":{"type":"array","items":{"$ref":"#/components/schemas/Node"}}}}}`,
			`{"properties":{"kids":{"items":{},"type":"array"},"parent":{}},"type":"object"}`,
		},
		"a wrapper around an uncut ref stays": {
			`{"type":"object","nullable":true,"allOf":[{"$ref":"#/components/schemas/A"}]}`,
			`{"A":{"type":"object"}}`,
			`{"allOf":[{"type":"object"}],"nullable":true,"type":"object"}`,
		},
		"a cut ref in a wider schema is not a wrapper": {
			`{"$ref":"#/components/schemas/N"}`,
			`{"N":{"properties":{"p":{"nullable":true,"minProperties":1,"allOf":[{"$ref":"#/components/schemas/N"}]},"q":{"nullable":false,"allOf":[{"$ref":"#/components/schemas/N"}]},"r":{"allOf":[{"$ref":"#/components/schemas/N"},{}]}}}}`,
			`{"properties":{"p":{"allOf":[{}],"minProperties":1,"nullable":true},"q":{"allOf":[{}],"nullable":false},"r":{"allOf":[{},{}]}}}`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := `{"requests":[{"topic":"t","request":` + c.schema + `}],"components":{"schemas":` + c.components + `}}`
			want := `{"events":[],"requests":[{"request":` + c.want + `,"topic":"t"}]}`
			if got := project(t, doc, false); got != want {
				t.Errorf("\n got  %s\n want %s", got, want)
			}
		})
	}
}

func TestProjection_SortsByUTF16CodeUnits(t *testing.T) {
	// U+1F600 (a surrogate pair in UTF-16) sorts before U+FB33, though its UTF-8 bytes sort after.
	doc := `{"requests":[{"topic":"דּ","request":{"required":["דּ","😀"]}},{"topic":"😀","request":{}}]}`
	got := project(t, doc, false)
	if strings.Index(got, "\U0001F600\",\"דּ") < 0 {
		t.Errorf("required not in UTF-16 order: %s", got)
	}
	if strings.Index(got, `"topic":"`+"\U0001F600") > strings.Index(got, `"topic":"`+"דּ") {
		t.Errorf("entries not in UTF-16 order: %s", got)
	}
	if lessUTF16("a", "a") || !lessUTF16("a", "ab") || lessUTF16("ab", "a") {
		t.Error("lessUTF16 prefix ordering")
	}
}

func TestHashDocument(t *testing.T) {
	c1, r1, err := HashDocument([]byte(`{"requests":[{"topic":"a","request":{}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c2, r2, err := HashDocument([]byte(`{"requests":[{"topic":"a","request":{"title":"x"}}],"sends":[],"service":"other"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 || r1 != r2 || c1 == r1 || len(c1) != len(HashPrefix)+64 {
		t.Errorf("hashes: (%s, %s) vs (%s, %s)", c1, r1, c2, r2)
	}
	for _, bad := range []string{`[]`, `null`, `{`, `{"requests":[{"topic":"a","request":{"maximum":1e999}}]}`, `{"requests":[{"topic":"a","request":{}}],"sends":[{"topic":"b","request":{"maximum":1e999}}]}`} {
		if _, _, err := HashDocument([]byte(bad)); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestDocumentHashes_MatchHashDocument(t *testing.T) {
	doc := Document{SchemaDialect: SchemaDialect, Service: "s", Requests: []Request{{Topic: "a", Request: Schema{"type": "string"}}}}
	raw, _ := json.Marshal(doc)
	c1, r1, _ := HashDocument(raw)
	c2, r2, err := doc.Hashes()
	if err != nil || c1 != c2 || r1 != r2 {
		t.Errorf("Hashes() = (%s, %s, %v), HashDocument = (%s, %s)", c2, r2, err, c1, r1)
	}
}
