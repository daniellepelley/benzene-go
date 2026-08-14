package gengo

import (
	"fmt"
	"unicode"
)

// goKeywords is the Go language's reserved word set (https://go.dev/ref/spec#Keywords) - a
// package name or exported identifier this package derives must not collide with one of these.
var goKeywords = map[string]bool{
	"break": true, "default": true, "func": true, "interface": true, "select": true,
	"case": true, "defer": true, "go": true, "map": true, "struct": true,
	"chan": true, "else": true, "goto": true, "package": true, "switch": true,
	"const": true, "fallthrough": true, "if": true, "range": true, "type": true,
	"continue": true, "for": true, "import": true, "return": true, "var": true,
}

// ValidateGoIdentifier reports an error if s is not a legal Go identifier (parity checklist row
// 7: the Contract Document's namespace/module setting becomes the generated Go package name,
// "used exactly - validate it's a legal Go identifier, fail loud if not").
func ValidateGoIdentifier(s string) error {
	if s == "" {
		return fmt.Errorf("gengo: identifier is empty")
	}
	for i, r := range s {
		switch {
		case i == 0 && !(unicode.IsLetter(r) || r == '_'):
			return fmt.Errorf("gengo: identifier %q must start with a letter or underscore, not %q", s, r)
		case i > 0 && !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'):
			return fmt.Errorf("gengo: identifier %q contains the illegal character %q", s, r)
		}
	}
	if goKeywords[s] {
		return fmt.Errorf("gengo: identifier %q is a Go keyword", s)
	}
	return nil
}
