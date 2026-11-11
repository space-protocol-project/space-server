package valueschema

import "testing"

func TestValueSchemaInteger(t *testing.T) {
	s, err := Decode([]byte(`{"type":"integer","minimum":-10,"maximum":10}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"1", "-10", "1.0", "1e1"} {
		if s.Validate([]byte(raw)) != nil {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"11", "1.00000000000000000001", "1e999999999", "true", "null"} {
		if s.Validate([]byte(raw)) == nil {
			t.Fatal(raw)
		}
	}
}

func TestValueSchema(t *testing.T) {
	s, err := Decode([]byte(`{"type":"object","properties":{"rows":{"type":"array","maxItems":2,"items":{"type":"string","maxLength":3}},"ok":{"type":"boolean"}},"required":["rows","ok"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Validate([]byte(`{"rows":["😀"],"ok":true}`)) != nil {
		t.Fatal("Корректное значение отклонено")
	}
	for _, raw := range []string{`{}`, `{"rows":[],"ok":null}`, `{"rows":[],"ok":true,"extra":1}`, `{"rows":["long"],"ok":true}`, `{"rows":["a","b","c"],"ok":true}`, `{"rows":[],"ok":true,"ok":false}`} {
		if s.Validate([]byte(raw)) == nil {
			t.Fatal("Принято неверное значение", raw)
		}
	}
}

func TestValueSchemaRejects(t *testing.T) {
	for _, raw := range []string{`null`, `{"$ref":"https://example.com"}`, `{"type":"string","maxLength":4097}`, `{"type":"string","maxLength":3,"pattern":".*"}`, `{"type":"string","maxLength":3,"enum":[]}`, `{"type":"object","properties":{},"required":[],"additionalProperties":true}`, `{"type":"object","properties":{},"required":["missing"],"additionalProperties":false}`, `{"type":"array","items":{"type":"boolean"},"maxItems":129}`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal("Принята неверная схема", raw)
		}
	}
	raw := `{"type":"boolean"}`
	for range 10 {
		raw = `{"type":"array","items":` + raw + `,"maxItems":1}`
	}
	if _, err := Decode([]byte(raw)); err == nil {
		t.Fatal("Не ограничена глубина")
	}
}
