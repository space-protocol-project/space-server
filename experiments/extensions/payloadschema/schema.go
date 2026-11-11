// Package payloadschema реализует ограниченный проверяемый профиль форм P1.
// Ссылки, регулярные выражения, вложенные объекты и исполняемые выражения запрещены.
package payloadschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"unicode/utf8"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

var ErrSchema = errors.New("unsupported_payload_schema")
var name = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type Field struct {
	Type      string   `json:"type"`
	MaxLength int      `json:"maxLength,omitempty"`
	MinLength int      `json:"minLength,omitempty"`
	Enum      []string `json:"enum,omitempty"`
}
type Schema struct {
	Type                 string           `json:"type"`
	Properties           map[string]Field `json:"properties"`
	Required             []string         `json:"required"`
	AdditionalProperties bool             `json:"additionalProperties"`
}

func Decode(raw []byte) (Schema, error) {
	var s Schema
	if extensions.CheckJSON(raw) != nil {
		return s, ErrSchema
	}
	fields, err := object(raw, []string{"type", "properties", "required", "additionalProperties"})
	if err != nil || len(fields) != 4 {
		return s, ErrSchema
	}
	if json.Unmarshal(raw, &s) != nil || s.Type != "object" || s.AdditionalProperties || s.Properties == nil || s.Required == nil || len(s.Properties) > 16 {
		return s, ErrSchema
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(fields["properties"], &properties) != nil {
		return s, ErrSchema
	}
	for key, field := range properties {
		if !name.MatchString(key) || validField(field, s.Properties[key]) != nil {
			return Schema{}, ErrSchema
		}
	}
	seen := map[string]bool{}
	for _, key := range s.Required {
		if _, ok := s.Properties[key]; !ok || seen[key] {
			return Schema{}, ErrSchema
		}
		seen[key] = true
	}
	return s, nil
}

func object(raw []byte, allowed []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrSchema
	}
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key, value := range fields {
		if !set[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrSchema
		}
	}
	return fields, nil
}

func validField(raw []byte, f Field) error {
	fields, err := object(raw, []string{"type", "minLength", "maxLength", "enum"})
	if err != nil || fields["type"] == nil {
		return ErrSchema
	}
	if f.Type == "boolean" {
		if len(fields) != 1 {
			return ErrSchema
		}
		return nil
	}
	if f.Type != "string" || fields["maxLength"] == nil || f.MaxLength < 1 || f.MaxLength > 512 || f.MinLength < 0 || f.MinLength > f.MaxLength {
		return ErrSchema
	}
	if fields["enum"] != nil && (len(f.Enum) == 0 || len(f.Enum) > 16) {
		return ErrSchema
	}
	seen := map[string]bool{}
	for _, value := range f.Enum {
		if !f.validStringLength(value) || seen[value] {
			return ErrSchema
		}
		seen[value] = true
	}
	return nil
}

func (f Field) validStringLength(value string) bool {
	n := utf8.RuneCountInString(value)
	return n >= f.MinLength && n <= f.MaxLength
}

func (s Schema) Validate(raw []byte) error {
	if extensions.CheckJSON(raw) != nil {
		return ErrSchema
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return ErrSchema
	}
	for _, key := range s.Required {
		if _, ok := values[key]; !ok {
			return ErrSchema
		}
	}
	for key, value := range values {
		field, ok := s.Properties[key]
		if !ok || !field.accepts(value) {
			return ErrSchema
		}
	}
	return nil
}

func (f Field) accepts(raw []byte) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if f.Type == "boolean" {
		var value bool
		return json.Unmarshal(raw, &value) == nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !f.validStringLength(value) {
		return false
	}
	if len(f.Enum) == 0 {
		return true
	}
	for _, item := range f.Enum {
		if item == value {
			return true
		}
	}
	return false
}
