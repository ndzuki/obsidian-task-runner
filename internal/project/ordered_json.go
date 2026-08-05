package project

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// orderedJSON preserves source field order through parse-modify-marshal, so
// daemon maintenance of vault-map.json does not reshuffle the hand-curated
// field layout (users reorder sections for readability; encoding/json maps
// would alphabetize them).
type orderedJSON struct {
	fields []orderedField
}

type orderedField struct {
	key   string
	value any // string | json.Number | bool | nil | []any | *orderedJSON
}

// parseOrderedJSON parses a JSON object preserving key order.
func parseOrderedJSON(data []byte) (*orderedJSON, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*orderedJSON)
	if !ok {
		return nil, fmt.Errorf("root value must be a JSON object")
	}
	return obj, nil
}

func parseOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &orderedJSON{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be a string")
				}
				val, err := parseOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				obj.fields = append(obj.fields, orderedField{key: key, value: val})
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			var arr []any
			for dec.More() {
				val, err := parseOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil
	}
}

// set replaces the value of key, preserving its position; appends when absent.
func (o *orderedJSON) set(key string, value any) {
	for i := range o.fields {
		if o.fields[i].key == key {
			o.fields[i].value = value
			return
		}
	}
	o.fields = append(o.fields, orderedField{key: key, value: value})
}

// get returns the value for key.
func (o *orderedJSON) get(key string) (any, bool) {
	for i := range o.fields {
		if o.fields[i].key == key {
			return o.fields[i].value, true
		}
	}
	return nil, false
}

// marshalOrderedJSON renders the object with 2-space indentation in source
// field order.
func marshalOrderedJSON(o *orderedJSON) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeOrderedValue(&buf, o, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeOrderedValue(buf *bytes.Buffer, v any, depth int) error {
	indent := func(n int) {
		for range n {
			buf.WriteString("  ")
		}
	}
	switch t := v.(type) {
	case *orderedJSON:
		if len(t.fields) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteString("{\n")
		for i, f := range t.fields {
			indent(depth + 1)
			keyJSON, _ := json.Marshal(f.key)
			buf.Write(keyJSON)
			buf.WriteString(": ")
			if err := writeOrderedValue(buf, f.value, depth+1); err != nil {
				return err
			}
			if i < len(t.fields)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		indent(depth)
		buf.WriteString("}")
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("[\n")
		for i, item := range t {
			indent(depth + 1)
			if err := writeOrderedValue(buf, item, depth+1); err != nil {
				return err
			}
			if i < len(t)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		indent(depth)
		buf.WriteString("]")
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(data)
	}
	return nil
}
