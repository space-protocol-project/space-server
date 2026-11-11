// Package valueschema проверяет ограниченные схемы результатов и событий P1.
// Нет ссылок, сети, regex и исполняемых выражений; все контейнеры ограничены.
package valueschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

var ErrSchema = errors.New("invalid_value_schema")

type Schema struct {
	Type                 string             `json:"type"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties bool               `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	MaxItems             int                `json:"maxItems,omitempty"`
	MaxLength            int                `json:"maxLength,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Minimum              int64              `json:"minimum,omitempty"`
	Maximum              int64              `json:"maximum,omitempty"`
}

func Decode(raw []byte) (*Schema, error) {
	if extensions.CheckJSON(raw) != nil {
		return nil, ErrSchema
	}
	budget := 128
	return decode(raw, 0, &budget)
}

func decode(raw []byte, depth int, budget *int) (*Schema, error) {
	*budget--
	if depth > 8 || *budget < 0 {
		return nil, ErrSchema
	}
	var fields map[string]json.RawMessage
	var s Schema
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &s) != nil {
		return nil, ErrSchema
	}
	allowed := map[string][]string{
		"object":  {"type", "properties", "required", "additionalProperties"},
		"array":   {"type", "items", "maxItems"},
		"string":  {"type", "maxLength", "enum"},
		"boolean": {"type"},
		"integer": {"type", "minimum", "maximum"},
	}
	keys, ok := allowed[s.Type]
	if !ok || !validKeys(fields, keys, s.Type == "string") {
		return nil, ErrSchema
	}
	switch s.Type {
	case "object":
		return decodeObject(s, fields["properties"], depth, budget)
	case "array":
		if s.MaxItems < 1 || s.MaxItems > 128 {
			return nil, ErrSchema
		}
		child, err := decode(fields["items"], depth+1, budget)
		if err != nil {
			return nil, err
		}
		s.Items = child
	case "string":
		if !validStringSchema(s, fields) {
			return nil, ErrSchema
		}
	case "integer":
		if s.Minimum < -9007199254740991 || s.Maximum > 9007199254740991 || s.Minimum > s.Maximum {
			return nil, ErrSchema
		}
	}
	return &s, nil
}

func decodeObject(s Schema, raw []byte, depth int, budget *int) (*Schema, error) {
	if s.AdditionalProperties || s.Properties == nil || s.Required == nil || len(s.Properties) > 32 {
		return nil, ErrSchema
	}
	var props map[string]json.RawMessage
	if json.Unmarshal(raw, &props) != nil {
		return nil, ErrSchema
	}
	for key, child := range props {
		if len(key) == 0 || len(key) > 64 {
			return nil, ErrSchema
		}
		parsed, err := decode(child, depth+1, budget)
		if err != nil {
			return nil, err
		}
		s.Properties[key] = parsed
	}
	if !validRequired(s) {
		return nil, ErrSchema
	}
	return &s, nil
}

func validRequired(s Schema) bool {
	seen := map[string]bool{}
	for _, key := range s.Required {
		if s.Properties[key] == nil || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validKeys(fields map[string]json.RawMessage, allowed []string, optionalEnum bool) bool {
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
		if fields[key] == nil && !(optionalEnum && key == "enum") {
			return false
		}
	}
	for key, value := range fields {
		if !set[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func validStringSchema(s Schema, fields map[string]json.RawMessage) bool {
	if s.MaxLength < 1 || s.MaxLength > 4096 {
		return false
	}
	if fields["enum"] == nil {
		return true
	}
	if len(s.Enum) == 0 || len(s.Enum) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range s.Enum {
		if seen[v] || utf8.RuneCountInString(v) > s.MaxLength {
			return false
		}
		seen[v] = true
	}
	return true
}

func (s *Schema) Validate(raw []byte) error {
	if extensions.CheckJSON(raw) != nil {
		return ErrSchema
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || !s.accepts(value) {
		return ErrSchema
	}
	return nil
}

func (s *Schema) accepts(value any) bool {
	switch s.Type {
	case "object":
		v, ok := value.(map[string]any)
		return ok && s.acceptsObject(v)
	case "array":
		v, ok := value.([]any)
		return ok && s.acceptsArray(v)
	case "string":
		v, ok := value.(string)
		return ok && s.acceptsString(v)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		v, ok := value.(json.Number)
		return ok && s.acceptsInteger(v)
	}
	return false
}

func (s *Schema) acceptsArray(values []any) bool {
	if len(values) > s.MaxItems {
		return false
	}
	for _, v := range values {
		if !s.Items.accepts(v) {
			return false
		}
	}
	return true
}

func (s *Schema) acceptsString(v string) bool {
	if utf8.RuneCountInString(v) > s.MaxLength {
		return false
	}
	if len(s.Enum) == 0 {
		return true
	}
	for _, item := range s.Enum {
		if v == item {
			return true
		}
	}
	return false
}

func (s *Schema) acceptsInteger(v json.Number) bool {
	if !boundedNumber(string(v)) {
		return false
	}
	n, ok := new(big.Rat).SetString(string(v))
	if !ok || !n.IsInt() || !n.Num().IsInt64() {
		return false
	}
	value := n.Num().Int64()
	return value >= s.Minimum && value <= s.Maximum
}

func boundedNumber(v string) bool {
	if len(v) > 32 {
		return false
	}
	index := strings.IndexAny(v, "eE")
	if index < 0 {
		return true
	}
	exponent, err := strconv.Atoi(v[index+1:])
	return err == nil && exponent >= -32 && exponent <= 32
}

func (s *Schema) acceptsObject(v map[string]any) bool {
	for _, key := range s.Required {
		if _, ok := v[key]; !ok {
			return false
		}
	}
	for key, value := range v {
		child := s.Properties[key]
		if child == nil || !child.accepts(value) {
			return false
		}
	}
	return true
}
