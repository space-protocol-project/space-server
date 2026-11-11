package payloadschema

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedFormPayload(t *testing.T) {
	raw, err := os.ReadFile("testdata/form-payload.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile("testdata/form-payload.json")
	if err != nil || s.Validate(valid) != nil {
		t.Fatal(err, string(valid))
	}
	for _, value := range []string{
		`{}`, `null`, `[]`,
		`{"message":"ok","kind":"a","enabled":true,"principal_id":"admin"}`,
		`{"message":"","kind":"a","enabled":true}`,
		`{"message":"ok","kind":"c","enabled":true}`,
		`{"message":"ok","kind":"a","enabled":"true"}`,
		`{"message":"ok","kind":"a","enabled":null}`,
		`{"message":"ok","kind":"a","kind":"b","enabled":true}`,
	} {
		if s.Validate([]byte(value)) == nil {
			t.Fatal(value)
		}
	}
	for n := 64; n <= 65; n++ {
		message := ""
		for i := 0; i < n; i++ {
			message += "😀"
		}
		value, _ := json.Marshal(map[string]any{"message": message, "kind": "a", "enabled": false})
		if (s.Validate(value) == nil) != (n == 64) {
			t.Fatal(n)
		}
	}
}

func TestFormSchemaRejects(t *testing.T) {
	for _, field := range []string{
		`{"type":"string"}`, `{"type":"string","maxLength":513}`,
		`{"type":"string","maxLength":2,"minLength":3}`,
		`{"type":"string","maxLength":2,"pattern":".*"}`,
		`{"type":"string","maxLength":2,"enum":[]}`,
		`{"type":"string","maxLength":2,"enum":["a","a"]}`,
		`{"type":"string","maxLength":2,"enum":["abc"]}`,
		`{"type":"string","maxLength":2,"enum":null}`,
		`{"type":"boolean","maxLength":2}`, `{"type":"number"}`,
		`{"type":"object","properties":{}}`, `{"$ref":"https://example.com"}`,
	} {
		raw := []byte(`{"type":"object","properties":{"field":` + field + `},"required":["field"],"additionalProperties":false}`)
		if _, err := Decode(raw); err == nil {
			t.Fatal(field)
		}
	}
	for _, raw := range []string{
		`{"type":"object","properties":{},"required":[],"additionalProperties":true}`,
		`{"type":"object","properties":{},"required":[],"additionalProperties":null}`,
		`{"type":"object","properties":{},"required":["missing"],"additionalProperties":false}`,
		`{"type":"object","properties":{"a":{"type":"boolean"}},"required":["a","a"],"additionalProperties":false}`,
		`{"type":"object","properties":null,"required":[],"additionalProperties":false}`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
