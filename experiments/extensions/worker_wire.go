package extensions

import (
	"encoding/json"
	"io"
)

// Внутренний протокол не является публичным ABI расширений.
// Проверка выполняется до создания runtime и компиляции.
func readWorkerRequest(input io.Reader) (workerRequest, error) {
	var request workerRequest
	raw, err := io.ReadAll(io.LimitReader(input, maxWire+1))
	if err != nil {
		return request, err
	}
	if len(raw) > maxWire {
		return request, ErrQuota
	}
	if err := checkJSONLimit(raw, maxWire); err != nil {
		return request, err
	}
	if err := checkWorkerFields(raw); err != nil {
		return request, err
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, ErrJSON
	}
	if request.Protocol != workerProtocol {
		return request, ErrABI
	}
	if len(request.WASM) == 0 || len(request.WASM) > MaxModule {
		return request, ErrQuota
	}
	return request, CheckJSON(request.Request)
}

func checkWorkerFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return ErrJSON
	}
	for _, name := range []string{"protocol", "wasm", "request", "interpreter", "allow_wait"} {
		value, exists := fields[name]
		if !exists || string(value) == "null" {
			return ErrJSON
		}
	}
	for _, name := range []string{"protocol", "wasm", "request"} {
		if fields[name][0] != '"' {
			return ErrJSON
		}
	}
	return nil
}
