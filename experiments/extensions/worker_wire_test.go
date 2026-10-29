package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func wireFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(workerRequest{Protocol: workerProtocol, WASM: []byte("fixture"), Request: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWorkerWireRejectsMalformed(t *testing.T) {
	valid := string(wireFixture(t))
	cases := map[string]string{
		"duplicate":    strings.Replace(valid, `"allow_wait":false`, `"allow_wait":true,"allow_wait":false`, 1),
		"case_alias":   strings.Replace(valid, `"wasm"`, `"WASM"`, 1),
		"unknown":      strings.Replace(valid, `"allow_wait":false`, `"other":false`, 1),
		"missing":      strings.Replace(valid, `,"allow_wait":false`, ``, 1),
		"null_flag":    strings.Replace(valid, `"allow_wait":false`, `"allow_wait":null`, 1),
		"wrong_flag":   strings.Replace(valid, `"allow_wait":false`, `"allow_wait":"false"`, 1),
		"version":      strings.Replace(valid, workerProtocol, "space.worker.p2", 1),
		"trailing":     valid + `{}`,
		"null":         `null`,
		"array":        `[]`,
		"invalid_utf8": valid[:len(valid)-1] + string([]byte{255}) + `}`,
		"bad_base64":   strings.Replace(valid, `"wasm":"Zml4dHVyZQ=="`, `"wasm":"!"`, 1),
		"byte_array":   strings.Replace(valid, `"wasm":"Zml4dHVyZQ=="`, `"wasm":[1,2,3]`, 1),
		"empty_wasm":   strings.Replace(valid, `"wasm":"Zml4dHVyZQ=="`, `"wasm":""`, 1),
		"bad_request":  strings.Replace(valid, `"request":"e30="`, `"request":"ew=="`, 1),
		"wire_limit":   strings.Repeat(" ", maxWire+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readWorkerRequest(strings.NewReader(input)); err == nil {
				t.Fatal("Повреждённое сообщение прошло проверку до компиляции")
			}
			var output bytes.Buffer
			if err := Worker(strings.NewReader(input), &output); err == nil {
				t.Fatal("Повреждённое сообщение принято")
			}
			if output.Len() != 0 {
				t.Fatal("Ошибка не должна выдавать частичный ответ")
			}
		})
	}
}

func TestWorkerWireLimits(t *testing.T) {
	request := workerRequest{Protocol: workerProtocol, WASM: bytes.Repeat([]byte{1}, MaxModule), Request: []byte(`"` + strings.Repeat("x", MaxJSON-2) + `"`)}
	raw, _ := json.Marshal(request)
	if _, err := readWorkerRequest(bytes.NewReader(raw)); err != nil {
		t.Fatal("Граничные размеры отклонены", err)
	}
	request.WASM = append(request.WASM, 1)
	raw, _ = json.Marshal(request)
	if _, err := readWorkerRequest(bytes.NewReader(raw)); !errors.Is(err, ErrQuota) {
		t.Fatal("Лимит модуля не применён", err)
	}
	request.WASM = []byte("fixture")
	request.Request = append(request.Request, ' ')
	raw, _ = json.Marshal(request)
	if _, err := readWorkerRequest(bytes.NewReader(raw)); !errors.Is(err, ErrJSON) {
		t.Fatal("Лимит JSON не применён", err)
	}
}

func TestWorkerWireRoundTrip(t *testing.T) {
	wasm, err := os.ReadFile("testdata/echo.wasm")
	if err != nil {
		t.Fatal(err)
	}
	for _, interpreter := range []bool{false, true} {
		raw, _ := json.Marshal(workerRequest{Protocol: workerProtocol, WASM: wasm, Request: []byte(`{"ok":true}`), Interpreter: interpreter})
		var output bytes.Buffer
		if err := Worker(bytes.NewReader(raw), &output); err != nil {
			t.Fatal(err)
		}
		if output.String() != `{"ok":true}` {
			t.Fatal(output.String())
		}
	}
}
