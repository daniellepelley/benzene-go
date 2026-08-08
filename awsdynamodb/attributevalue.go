package awsdynamodb

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// attributeMapToJSON unmarshals a DynamoDB AttributeValue map - an item image or key set in the
// stream's type-descriptor format, {"Id":{"N":"101"},"Msg":{"S":"hi"}} - into plain JSON,
// {"Id":101,"Msg":"hi"}, so a handler deserializes an ordinary struct instead of the descriptor
// format. Mirrors the .NET reference's DynamoDbAttributeValueConverter. Object keys are emitted in
// Go's map order (sorted), which is irrelevant to a struct-deserializing consumer.
func attributeMapToJSON(attributeMap json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(attributeMap, &m); err != nil {
		return nil, fmt.Errorf("awsdynamodb: malformed AttributeValue map: %w", err)
	}
	out := make(map[string]json.RawMessage, len(m))
	for name, attributeValue := range m {
		converted, err := convertAttributeValue(attributeValue)
		if err != nil {
			return nil, err
		}
		out[name] = converted
	}
	return json.Marshal(out)
}

// convertAttributeValue converts one AttributeValue - a single type-descriptor object such as
// {"N":"101"} or {"S":"hi"} - to its plain-JSON value.
func convertAttributeValue(attributeValue json.RawMessage) (json.RawMessage, error) {
	var descriptors map[string]json.RawMessage
	if err := json.Unmarshal(attributeValue, &descriptors); err != nil {
		return nil, fmt.Errorf("awsdynamodb: malformed AttributeValue: %w", err)
	}
	for descriptor, value := range descriptors { // an AttributeValue carries exactly one descriptor
		switch descriptor {
		case "S", "B":
			// A string, or a base64-encoded binary scalar: already a JSON string on the wire.
			return value, nil
		case "N":
			return numberLiteral(value)
		case "BOOL":
			// Already a JSON boolean.
			return value, nil
		case "NULL":
			return json.RawMessage("null"), nil
		case "M":
			return attributeMapToJSON(value)
		case "L":
			return convertList(value)
		case "SS", "BS":
			// A string set or a base64 binary set: already a JSON array of strings.
			return value, nil
		case "NS":
			return convertNumberSet(value)
		default:
			// Unknown descriptor: pass the raw value through rather than failing, so a new
			// DynamoDB type doesn't break existing consumers (matches the .NET reference).
			return value, nil
		}
	}
	return json.RawMessage("null"), nil // an empty AttributeValue object
}

// numberLiteral turns a DynamoDB number (a JSON string like "101" that is a valid JSON number
// literal) into a JSON number.
func numberLiteral(value json.RawMessage) (json.RawMessage, error) {
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return nil, fmt.Errorf("awsdynamodb: DynamoDB number is not a string: %w", err)
	}
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("awsdynamodb: DynamoDB number %q is not a valid JSON number", s)
	}
	return json.RawMessage(s), nil
}

// convertList converts an AttributeValue list ([{"S":"a"},{"N":"1"}]) to a plain JSON array.
func convertList(value json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, fmt.Errorf("awsdynamodb: malformed AttributeValue list: %w", err)
	}
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		converted, err := convertAttributeValue(item)
		if err != nil {
			return nil, err
		}
		out[i] = converted
	}
	return json.Marshal(out)
}

// convertNumberSet converts a DynamoDB number set (["1","2"]) to a plain JSON array of numbers.
func convertNumberSet(value json.RawMessage) (json.RawMessage, error) {
	var items []string
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, fmt.Errorf("awsdynamodb: malformed AttributeValue number set: %w", err)
	}
	out := make([]json.RawMessage, len(items))
	for i, s := range items {
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("awsdynamodb: DynamoDB number %q in a number set is not a valid JSON number", s)
		}
		out[i] = json.RawMessage(s)
	}
	return json.Marshal(out)
}

// isJSONObject reports whether raw is a JSON object (used to pick the first present image).
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
