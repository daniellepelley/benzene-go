package jcs

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// The number serialization samples of RFC 8785 Appendix B: IEEE 754 bit patterns and the string
// ECMAScript writes for each.
func TestAppendNumber_RFC8785AppendixB(t *testing.T) {
	cases := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, c := range cases {
		got, err := appendNumber(nil, math.Float64frombits(c.bits))
		if err != nil {
			t.Fatalf("%#x: %v", c.bits, err)
		}
		if string(got) != c.want {
			t.Errorf("%#x: got %s, want %s", c.bits, got, c.want)
		}
	}
}

func TestAppendNumber_RejectsNonFinite(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := appendNumber(nil, f); err == nil {
			t.Errorf("%v: expected an error", f)
		}
	}
}

// RFC 8785 §3.2.2's sample: whitespace removed, members sorted, numbers and strings normalized.
func TestTransform_RFC8785Sample(t *testing.T) {
	input := `{
  "numbers": [333333333.33333329, 1E30, 4.50,
              2e-3, 0.000000000000000000000000001],
  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
  "literals": [null, true, false]
}`
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	got, err := Transform([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// RFC 8785 §3.2.3's sorting sample: names compare by UTF-16 code units, so the surrogate pair of
// U+1F600 sorts before U+FB33 although its UTF-8 bytes sort after.
func TestTransform_SortsByUTF16CodeUnits(t *testing.T) {
	input := `{"€": "Euro Sign", "\r": "Carriage Return", "דּ": "Hebrew Letter Dalet With Dagesh",
"1": "One", "😀": "Emoji: Grinning Face", "\u0080": "Control", "ö": "Latin Small Letter O With Diaeresis"}`
	got, err := Transform([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	dec := json.NewDecoder(strings.NewReader(string(got)))
	dec.Token() // {
	for dec.More() {
		k, _ := dec.Token()
		v, _ := dec.Token()
		_ = k
		order = append(order, v.(string))
	}
	want := []string{"Carriage Return", "One", "Control", "Latin Small Letter O With Diaeresis", "Euro Sign", "Emoji: Grinning Face", "Hebrew Letter Dalet With Dagesh"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("order = %v\nwant    %v", order, want)
	}
}

func TestTransform_EscapesControlsOnly(t *testing.T) {
	got, err := Transform([]byte(`["\u0000\u001f\u007f <>&\b\t\n\f\r"]`))
	if err != nil {
		t.Fatal(err)
	}
	want := "[\"\\u0000\\u001f\u007f <>&\\b\\t\\n\\f\\r\"]"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTransform_Rejects(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate name":              `{"a":1,"a":2}`,
		"trailing data":               `{} {}`,
		"invalid utf-8":               "\"\xff\"",
		"out of range":                `[1e400]`,
		"not json":                    `{"a":}`,
		"empty":                       ``,
		"truncated":                   `[1,`,
		"open object":                 `{"a":1`,
		"lone high surrogate":         `["\ud83d"]`,
		"high surrogate then non-low": `["\ud83d\u0041"]`,
		"high surrogate then non-u":   `["\ud83d\n"]`,
		"lone low surrogate":          `{"\ude00":1}`,
		"high surrogate at end":       `["\ud83d`,
		"non-string key":              `{1:2}`,
		"object closed by bracket":    `{"a":1]`,
		"array closed by brace":       `[1}`,
		"bare closer":                 `]`,
	} {
		if _, err := Transform([]byte(input)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestTransform_AcceptsEscapesAndPairs(t *testing.T) {
	got, err := Transform([]byte(`["\ud83d\ude00", "\u0041\"\\", "\uZZZZ is the decoder's to reject"]`))
	if err == nil {
		t.Fatalf("an invalid \\u escape must still be rejected (by the decoder), got %s", got)
	}
	got, err = Transform([]byte(`["\ud83d\ude00", "\u0041\"\\x"]`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[\"\U0001F600\",\"A\\\"\\\\x\"]"; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestAppend_GenericValues(t *testing.T) {
	v := map[string]any{
		"f": 1.5, "f32": float32(0.5), "i": 3, "i64": int64(-4), "n": json.Number("10.0"),
		"maps": []map[string]any{{"b": true, "a": false}}, "strs": []string{"y", "x"},
	}
	got, err := Append(nil, v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"f":1.5,"f32":0.5,"i":3,"i64":-4,"maps":[{"a":false,"b":true}],"n":10,"strs":["y","x"]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := Append(nil, struct{}{}); err == nil {
		t.Error("expected an error for an unsupported type")
	}
	if _, err := Append(nil, []any{math.NaN()}); err == nil {
		t.Error("expected an error for NaN inside an array")
	}
	if _, err := Append(nil, map[string]any{"a": math.Inf(1)}); err == nil {
		t.Error("expected an error for Inf inside an object")
	}
	if _, err := Append(nil, json.Number("1e999")); err == nil {
		t.Error("expected an error for an out-of-range number")
	}
}
