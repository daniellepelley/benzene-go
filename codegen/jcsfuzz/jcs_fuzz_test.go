// Package jcsfuzz differentially fuzzes the root module's hand-written RFC 8785 canonicalizer
// (internal/jcs, which the contract hashes run on) against github.com/gowebpki/jcs. The root module
// stays dependency-free, so the comparison lives here, in the one module that already depends on
// gowebpki/jcs (self-description-design §10.5, ruling Q8). `go test` runs the seed corpus; run
// `go test -fuzz=FuzzTransform ./jcsfuzz` to search further.
package jcsfuzz

import (
	"bytes"
	"testing"

	"github.com/daniellepelley/benzene-go/internal/jcs"
	gowebpki "github.com/gowebpki/jcs"
)

var seeds = []string{
	`{}`, `[]`, `null`, `true`, `false`, `0`, `-0`, `1`, `-1.5`, `1e21`, `1e-7`, `123456789012345678901234567890`,
	`0.1`, `5e-324`, `1.7976931348623157e308`, `9007199254740993`, `333333333.33333329`, `1E30`, `4.50`, `2e-3`,
	`"a\u20ac\u000f\n\"\\/\b\f\r\t\u001f\u007f\u2028"`,
	`{"b":1,"a":[{"z":null,"y":true}],"\u20ac":"e","\r":"cr","\ud83d\ude00":"emoji","\ufb33":"dalet"}`,
	`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"literals":[null,true,false]}`,
	"  { \"spaced\" : [ 1 , 2 ] }  ",
	`{"requests":[{"topic":"t","request":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}]}`,
}

func FuzzTransform(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		ours, ourErr := jcs.Transform(input)
		theirs, theirErr := gowebpki.Transform(input)
		if ourErr != nil || theirErr != nil {
			// The two parsers reject different malformed inputs (gowebpki's is its own; ours is
			// encoding/json's); what must agree is the canonical form of every input both accept.
			return
		}
		if !bytes.Equal(ours, theirs) {
			t.Fatalf("canonical forms differ for %q:\n internal/jcs %s\n gowebpki/jcs %s", input, ours, theirs)
		}
	})
}

// TestSeedsAreAcceptedByBoth keeps the fuzz seed corpus meaningful: every seed must be valid JSON to
// both implementations, or the differential check above compares nothing for it.
func TestSeedsAreAcceptedByBoth(t *testing.T) {
	for _, s := range seeds {
		if _, err := jcs.Transform([]byte(s)); err != nil {
			t.Errorf("internal/jcs rejects seed %s: %v", s, err)
		}
		if _, err := gowebpki.Transform([]byte(s)); err != nil {
			t.Errorf("gowebpki/jcs rejects seed %s: %v", s, err)
		}
	}
}
