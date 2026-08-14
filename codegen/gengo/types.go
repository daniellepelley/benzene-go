package gengo

import (
	"fmt"
	"sort"
	"strings"
)

// TypeDecl is one generated Go type declaration (a struct DTO, or - for a oneOf-with-
// discriminator union - a marker interface).
type TypeDecl struct {
	// Name is the schema's catalogue name (unformatted - the wire/$ref name).
	Name string
	// GoName is the exported Go type name (FormatGoName(Name)).
	GoName string
	// Source is the full Go declaration, e.g. "type Foo struct {\n\t...\n}".
	Source string
	// UsesTime reports whether Source references time.Time, so the caller knows to import
	// "time".
	UsesTime bool
}

// BuildTypeDecls builds one Go type declaration per entry in schemas (typically an already
// topic-scoped or otherwise narrowed components.schemas map - see contractdoc.ReachableSchemas),
// sorted by name for deterministic output (component declaration order in the source document
// carries no meaning per contract-document.md §4, so this port picks a stable order rather than
// depending on Go map iteration or JSON object order).
//
// Composition mirrors the .NET reference (OpenApiSchemaCSharpTypeBuilder), translated to Go's own
// idiom at each point that differs:
//   - allOf inheritance (a single $ref branch as the "base", inline branches contributing their
//     own properties) becomes Go struct embedding: the base type is embedded anonymously, so
//     encoding/json flattens it into the same JSON object on both marshal and unmarshal - the
//     honest Go equivalent of the reference's C# base class.
//   - oneOf-with-a-shared-discriminator becomes an unexported marker-method interface
//     (Go has no union type): every subtype the discriminator's mapping names gets an
//     `is<Union>()` method, sealing the interface to just those subtypes - the honest Go
//     equivalent of the reference's JsonPolymorphic/JsonDerivedType attributes.
//   - a discriminator's own property is skipped as an ordinary struct field (it is serializer
//     metadata, matching the reference).
//   - required-vs-optional is this port's own addition, absent from the reference: a property
//     listed in its schema's own "required" array (and not itself "nullable": true) becomes a
//     non-pointer field with no "omitempty"; everything else becomes a pointer (or, for a slice/
//     map field, is left as-is - already nil-able - with "omitempty") per this repo's existing
//     struct/json-tag convention (examples/http-helloworld's greetRequest).
func BuildTypeDecls(schemas map[string]any) ([]TypeDecl, error) {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	unionOf := discriminatorUnions(schemas)

	decls := make([]TypeDecl, 0, len(names))
	for _, name := range names {
		schema, _ := schemas[name].(map[string]any)
		decl, err := buildTypeDecl(name, schema, schemas, unionOf)
		if err != nil {
			return nil, fmt.Errorf("gengo: schema %q: %w", name, err)
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

// discriminatorUnions returns, for every schema with a "oneOf"+"discriminator" (contract-
// document.md's OpenAPI 3.0 discriminator polymorphism shape), a subtypeName -> unionSchemaName
// map built from the discriminator's mapping values (each a $ref or a bare schema name).
func discriminatorUnions(schemas map[string]any) map[string]string {
	unionOf := map[string]string{}
	for name, raw := range schemas {
		schema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, hasOneOf := schema["oneOf"].([]any); !hasOneOf {
			continue
		}
		disc, ok := schema["discriminator"].(map[string]any)
		if !ok {
			continue
		}
		propertyName, _ := disc["propertyName"].(string)
		mapping, _ := disc["mapping"].(map[string]any)
		if propertyName == "" || len(mapping) == 0 {
			continue
		}
		for _, v := range mapping {
			ref, _ := v.(string)
			if subtype := lastPathSegment(ref); subtype != "" {
				unionOf[subtype] = name
			}
		}
	}
	return unionOf
}

func lastPathSegment(s string) string {
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

func buildTypeDecl(name string, schema map[string]any, catalogue map[string]any, unionOf map[string]string) (TypeDecl, error) {
	goName := FormatGoName(name)

	oneOf, _ := schema["oneOf"].([]any)
	discriminator, _ := schema["discriminator"].(map[string]any)
	discriminatorProperty, _ := discriminator["propertyName"].(string)
	hasDiscriminator := len(oneOf) > 0 && discriminatorProperty != ""

	if hasDiscriminator {
		return TypeDecl{
			Name:   name,
			GoName: goName,
			Source: fmt.Sprintf("// %s is a closed union sealed to the types the contract document's discriminator names.\ntype %s interface {\n\tis%s()\n}", goName, goName, goName),
		}, nil
	}

	baseRef := allOfBaseRef(schema)
	properties, requiredSet := ownProperties(schema)

	propertyNames := make([]string, 0, len(properties))
	for propertyName := range properties {
		propertyNames = append(propertyNames, propertyName)
	}
	sort.Strings(propertyNames)

	var body strings.Builder
	usesTime := false

	if baseRef != "" {
		fmt.Fprintf(&body, "\t%s\n", FormatGoName(baseRef))
	}

	for _, propertyName := range propertyNames {
		if discriminatorProperty != "" && propertyName == discriminatorProperty {
			// The discriminator is serializer metadata on the *union* schema; skip it here too
			// in case a subtype's own schema happens to redeclare it as a property.
			continue
		}
		propertySchema, _ := properties[propertyName].(map[string]any)
		fieldType := GoTypeName(propertySchema, catalogue)
		if fieldType == "time.Time" {
			usesTime = true
		}

		nullable, _ := propertySchema["nullable"].(bool)
		required := requiredSet[propertyName] && !nullable
		omitEmpty := true
		if required {
			omitEmpty = false
		} else if !strings.HasPrefix(fieldType, "[]") && !strings.HasPrefix(fieldType, "map[") {
			fieldType = "*" + fieldType
		}

		tag := "json:\"" + propertyName
		if omitEmpty {
			tag += ",omitempty"
		}
		tag += "\""
		fmt.Fprintf(&body, "\t%s %s `%s`\n", FormatGoName(propertyName), fieldType, tag)
	}

	var source strings.Builder
	fmt.Fprintf(&source, "type %s struct {\n%s}", goName, body.String())

	if unionName, ok := unionOf[name]; ok {
		fmt.Fprintf(&source, "\n\nfunc (%s) is%s() {}", goName, FormatGoName(unionName))
	}

	return TypeDecl{Name: name, GoName: goName, Source: source.String(), UsesTime: usesTime}, nil
}

// ownProperties mirrors the reference's GetOwnProperties: a schema's own "properties" plus every
// inline (non-$ref) allOf branch's "properties", first occurrence winning on a key collision. The
// "required" set returned is the schema's own top-level "required" array only - the reference has
// no such concept at all (see BuildTypeDecls' doc comment); allOf branches' own "required" arrays
// are not merged in, matching how the reference does not merge them into GetOwnProperties either.
func ownProperties(schema map[string]any) (properties map[string]any, required map[string]bool) {
	properties = map[string]any{}

	if own, ok := schema["properties"].(map[string]any); ok {
		for k, v := range own {
			properties[k] = v
		}
	}

	if allOf, ok := schema["allOf"].([]any); ok {
		for _, member := range allOf {
			memberSchema, ok := member.(map[string]any)
			if !ok {
				continue
			}
			if _, isRef := memberSchema["$ref"]; isRef {
				continue
			}
			if inlineProps, ok := memberSchema["properties"].(map[string]any); ok {
				for k, v := range inlineProps {
					if _, exists := properties[k]; !exists {
						properties[k] = v
					}
				}
			}
		}
	}

	required = map[string]bool{}
	if reqList, ok := schema["required"].([]any); ok {
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}

	return properties, required
}
