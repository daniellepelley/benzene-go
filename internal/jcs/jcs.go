// Package jcs is the RFC 8785 JSON Canonicalization Scheme, hand-written so the root module stays
// dependency-free. The contract document's two hashes are compared across ports
// (contract-document.md §6.3), so this must agree with every other RFC 8785 implementation
// byte-for-byte: codegen's differential fuzz test (codegen/jcsfuzz) checks it against
// github.com/gowebpki/jcs, which that module already depends on, and the vendored
// contract-hash-cases.json pins the end-to-end values.
//
// The rules (RFC 8785 §3.2):
//
//   - objects: members sorted by their names' UTF-16 code units; a duplicate name is an error;
//   - strings: only `"`, `\` and the C0 controls are escaped - \b \t \n \f \r by their short form,
//     the rest as \u00xx with lower-case hex - and every other character is written as itself;
//   - numbers: the ECMAScript Number-to-String form of the IEEE 754 double the literal denotes;
//     NaN and the infinities are not JSON and are rejected;
//   - no insignificant whitespace.
package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Transform canonicalizes one JSON text. It rejects input that is not exactly one JSON value
// (trailing data included), invalid UTF-8, a duplicate member name, and a number outside the
// IEEE 754 double range.
func Transform(input []byte) ([]byte, error) {
	if !utf8.Valid(input) {
		return nil, errors.New("jcs: input is not valid UTF-8")
	}
	if err := checkSurrogates(input); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("jcs: trailing data after the JSON value")
	}
	return Append(nil, value)
}

// checkSurrogates rejects a \u escape of a lone UTF-16 surrogate: RFC 8785 canonicalizes I-JSON
// (RFC 7493), whose strings must be valid Unicode, and encoding/json would otherwise quietly turn
// the escape into U+FFFD - a different string, and so a different hash, from what the sender meant.
// It only looks inside string literals; the decoder rejects anything else that is malformed.
func checkSurrogates(input []byte) error {
	inString := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if !inString {
			inString = c == '"'
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 < len(input) && input[i+1] == 'u' {
				r, ok := hex4(input, i+2)
				switch {
				case !ok:
				case r >= 0xD800 && r <= 0xDBFF:
					low, ok := uint16(0), false
					if i+7 < len(input) && input[i+6] == '\\' && input[i+7] == 'u' {
						low, ok = hex4(input, i+8)
					}
					if !ok || low < 0xDC00 || low > 0xDFFF {
						return errors.New("jcs: a string holds a lone UTF-16 surrogate")
					}
					i += 6 // past the pair's first escape; the second is consumed below
				case r >= 0xDC00 && r <= 0xDFFF:
					return errors.New("jcs: a string holds a lone UTF-16 surrogate")
				}
			}
			i++ // the escaped character
		}
	}
	return nil
}

// hex4 reads four hex digits at input[at:].
func hex4(input []byte, at int) (uint16, bool) {
	if at+4 > len(input) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(input[at:at+4]), 16, 16)
	return uint16(v), err == nil
}

// decodeValue reads one value off dec's token stream, keeping objects as ordered member lists so a
// duplicate name can be detected rather than silently collapsed (encoding/json keeps the last).
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("jcs: %w", err)
				}
				key := keyTok.(string) // the decoder only yields a string in key position
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("jcs: duplicate member name %q", key)
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj[key] = v
			}
			if _, err := dec.Token(); err != nil { // the closing '}'
				return nil, fmt.Errorf("jcs: %w", err)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // the closing ']'
				return nil, fmt.Errorf("jcs: %w", err)
			}
			return arr, nil
		}
		// Unreachable: the decoder only yields a closing delimiter where it closes something,
		// and the loops above consume those. Kept so a decoder change fails loudly.
		return nil, fmt.Errorf("jcs: unexpected delimiter %q", t)
	default:
		return t, nil // string, json.Number, bool or nil
	}
}

// Append writes the canonical form of v to buf. v is a generic JSON tree: map[string]any, []any,
// string, json.Number, float64, the integer kinds, bool or nil - what encoding/json decodes into
// (with or without UseNumber), plus []map[string]any and []string for convenience.
func Append(buf []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(buf, "null"...), nil
	case bool:
		if t {
			return append(buf, "true"...), nil
		}
		return append(buf, "false"...), nil
	case string:
		return appendString(buf, t), nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return nil, fmt.Errorf("jcs: number %s: %w", t, err)
		}
		return appendNumber(buf, f)
	case float64:
		return appendNumber(buf, t)
	case float32:
		return appendNumber(buf, float64(t))
	case int:
		return appendNumber(buf, float64(t))
	case int64:
		return appendNumber(buf, float64(t))
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortUTF16(keys)
		buf = append(buf, '{')
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendString(buf, k)
			buf = append(buf, ':')
			var err error
			if buf, err = Append(buf, t[k]); err != nil {
				return nil, err
			}
		}
		return append(buf, '}'), nil
	case []any:
		buf = append(buf, '[')
		for i, e := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			var err error
			if buf, err = Append(buf, e); err != nil {
				return nil, err
			}
		}
		return append(buf, ']'), nil
	case []map[string]any:
		generic := make([]any, len(t))
		for i, e := range t {
			generic[i] = e
		}
		return Append(buf, generic)
	case []string:
		generic := make([]any, len(t))
		for i, e := range t {
			generic[i] = e
		}
		return Append(buf, generic)
	}
	return nil, fmt.Errorf("jcs: unsupported value of type %T", v)
}

// sortUTF16 orders names by their UTF-16 code units (RFC 8785 §3.2.3). That differs from Go's
// byte-wise (UTF-8) string order only for characters outside the Basic Multilingual Plane, whose
// surrogate pairs sort below U+E000-U+FFFF.
func sortUTF16(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		a, b := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

const hexDigits = "0123456789abcdef"

func appendString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			buf = append(buf, '\\', '"')
		case c == '\\':
			buf = append(buf, '\\', '\\')
		case c == '\b':
			buf = append(buf, '\\', 'b')
		case c == '\t':
			buf = append(buf, '\\', 't')
		case c == '\n':
			buf = append(buf, '\\', 'n')
		case c == '\f':
			buf = append(buf, '\\', 'f')
		case c == '\r':
			buf = append(buf, '\\', 'r')
		case c < 0x20:
			buf = append(buf, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			buf = append(buf, c) // every other byte, multi-byte UTF-8 sequences included, verbatim
		}
	}
	return append(buf, '"')
}

// appendNumber writes f in the ECMAScript Number::toString form (ECMA-262 §6.1.6.1.20), which
// RFC 8785 §3.2.2.3 adopts: the shortest digit string that round-trips (strconv's -1 precision),
// placed in plain notation for a decimal exponent in [-6, 21) and in exponent notation otherwise.
func appendNumber(buf []byte, f float64) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("jcs: %v is not a JSON number", f)
	}
	if f == 0 {
		return append(buf, '0'), nil // -0 included
	}
	if f < 0 {
		buf = append(buf, '-')
		f = -f
	}
	// 'e' with -1 precision: d[.ddd]e±xx with the shortest round-tripping digits.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	x, _ := strconv.Atoi(exp) // FormatFloat always writes a valid exponent
	k := len(digits)
	n := x + 1 // the decimal point sits after the n-th digit
	switch {
	case k <= n && n <= 21:
		buf = append(buf, digits...)
		buf = append(buf, strings.Repeat("0", n-k)...)
	case 0 < n && n <= 21:
		buf = append(buf, digits[:n]...)
		buf = append(buf, '.')
		buf = append(buf, digits[n:]...)
	case -6 < n && n <= 0:
		buf = append(buf, "0."...)
		buf = append(buf, strings.Repeat("0", -n)...)
		buf = append(buf, digits...)
	default:
		buf = append(buf, digits[0])
		if k > 1 {
			buf = append(buf, '.')
			buf = append(buf, digits[1:]...)
		}
		buf = append(buf, 'e')
		if n-1 >= 0 {
			buf = append(buf, '+')
		}
		buf = strconv.AppendInt(buf, int64(n-1), 10)
	}
	return buf, nil
}
