package gemini

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// jsonSchema turns a Gemini Schema (OpenAPI 3.0's subset: upper-case
// types, nullable, int64 limits as strings) into the JSON Schema Chat's
// tools and response formats take. Property names stay as they are.
func jsonSchema(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return marshal(convertSchema(v))
}

func convertSchema(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	nullable := false
	for k, val := range m {
		switch key := camel(k); key {
		case "type":
			s, ok := val.(string)
			switch {
			case !ok:
				out["type"] = val
			case s != "" && s != "TYPE_UNSPECIFIED":
				out["type"] = strings.ToLower(s)
			}
		case "nullable":
			nullable, _ = val.(bool)
		case "properties":
			props, ok := val.(map[string]any)
			if !ok {
				continue
			}
			p := make(map[string]any, len(props))
			for name, s := range props {
				p[name] = convertSchema(s)
			}
			out["properties"] = p
		case "items":
			out["items"] = convertSchema(val)
		case "anyOf":
			list, ok := val.([]any)
			if !ok {
				continue
			}
			alts := make([]any, len(list))
			for i, s := range list {
				alts[i] = convertSchema(s)
			}
			out["anyOf"] = alts
		case "minItems", "maxItems", "minProperties", "maxProperties", "minLength", "maxLength":
			out[key] = integer(val)
		case "format":
			if val != "enum" { // Gemini's marker for enums, not a JSON Schema format
				out["format"] = val
			}
		case "propertyOrdering", "example":
			// Gemini's order and example aren't JSON Schema.
		default:
			out[key] = val
		}
	}
	if nullable {
		if s, ok := out["type"].(string); ok {
			out["type"] = []any{s, "null"}
		}
	}
	return out
}

// integer is an int64 limit, which protobuf's JSON may write as a string.
func integer(v any) any {
	if s, ok := v.(string); ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	return v
}
