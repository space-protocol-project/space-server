package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var ErrJSON = errors.New("extension JSON")

func CheckJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxJSON || !utf8.Valid(raw) {
		return ErrJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := value(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrJSON
	}
	return nil
}

func value(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrJSON
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrJSON
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return ErrJSON
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return ErrJSON
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return ErrJSON
			}
			keys[name] = true
		}
		if err := value(decoder, depth+1); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return ErrJSON
	}
	return nil
}
