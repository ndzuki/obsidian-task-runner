// Package jsonorder provides order-preserving JSON object parsing and
// marshaling, used by daemon maintenance of vault-map.json (config load /
// project registration writes).
package jsonorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// OrderedJSON preserves source field order through parse-modify-marshal, so
// daemon maintenance of vault-map.json does not reshuffle the hand-curated
// field layout (users reorder sections for readability; encoding/json maps
// would alphabetize them).
type OrderedJSON struct {
	fields []orderedField
}

type orderedField struct {
	key   string
	value any // string | json.Number | bool | nil | []any | *OrderedJSON
}

// Parse parses a JSON object preserving key order.
func Parse(data []byte) (*OrderedJSON, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*OrderedJSON)
	if !ok {
		return nil, fmt.Errorf("root value must be a JSON object")
	}
	return obj, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &OrderedJSON{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be string")
				}
				val, err := parseValue(dec)
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
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter %v", t)
		}
	default:
		return tok, nil
	}
}

// AtomicWrite writes data to path via temp file + fsync + rename
// (crash-safe, used by vault-map.json maintenance).
func AtomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".otg-register-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("write temp: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("fsync: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// Field is one ordered key-value pair of an OrderedJSON.
type Field struct {
	Key   string
	Value any
}

// Fields returns the ordered key-value pairs.
func (o *OrderedJSON) Fields() []Field {
	out := make([]Field, len(o.fields))
	for i, f := range o.fields {
		out[i] = Field{Key: f.key, Value: f.value}
	}
	return out
}

// Set replaces the value of key, preserving its position; appends when absent.
func (o *OrderedJSON) Set(key string, value any) {
	for i := range o.fields {
		if o.fields[i].key == key {
			o.fields[i].value = value
			return
		}
	}
	o.fields = append(o.fields, orderedField{key: key, value: value})
}

// Get returns the value for key.
func (o *OrderedJSON) Get(key string) (any, bool) {
	for i := range o.fields {
		if o.fields[i].key == key {
			return o.fields[i].value, true
		}
	}
	return nil, false
}

// Delete removes key, preserving the order of the remaining fields.
func (o *OrderedJSON) Delete(key string) bool {
	for i := range o.fields {
		if o.fields[i].key == key {
			o.fields = append(o.fields[:i], o.fields[i+1:]...)
			return true
		}
	}
	return false
}

// Marshal renders the object with 2-space indentation in source field order.
func Marshal(o *OrderedJSON) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeValue(&buf, o, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeValue(buf *bytes.Buffer, v any, depth int) error {
	switch t := v.(type) {
	case *OrderedJSON:
		if len(t.fields) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteString("{\n")
		indent := depth + 1
		for i, f := range t.fields {
			if i > 0 {
				buf.WriteString(",\n")
			}
			for j := 0; j < indent; j++ {
				buf.WriteString("  ")
			}
			key, err := json.Marshal(f.key)
			if err != nil {
				return err
			}
			buf.Write(key)
			buf.WriteString(": ")
			if err := writeValue(buf, f.value, indent); err != nil {
				return err
			}
		}
		buf.WriteString("\n")
		for j := 0; j < depth; j++ {
			buf.WriteString("  ")
		}
		buf.WriteString("}")
		return nil
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("[\n")
		indent := depth + 1
		for i, item := range t {
			if i > 0 {
				buf.WriteString(",\n")
			}
			for j := 0; j < indent; j++ {
				buf.WriteString("  ")
			}
			if err := writeValue(buf, item, indent); err != nil {
				return err
			}
		}
		buf.WriteString("\n")
		for j := 0; j < depth; j++ {
			buf.WriteString("  ")
		}
		buf.WriteString("]")
		return nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}
