// Package demo — эксперимент; рабочий сервер его не импортирует.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

const Protocol = "space.dispatch.p1"

var ErrDenied = errors.New("permission_denied")
var ErrRequest = errors.New("invalid_request")
var ErrVersion = errors.New("unsupported_version")
var ErrExtension = errors.New("extension_failed")

// Principal формирует только доверенный адаптер авторизации.
type Principal struct{ Space, Instance, User, Device, Revision string }
type Authorize func(context.Context, string, string, bool) (Principal, error)
type Request struct {
	Protocol string                     `json:"protocol"`
	Instance string                     `json:"instance"`
	Kind     string                     `json:"kind"`
	Name     string                     `json:"name"`
	Payload  map[string]json.RawMessage `json:"payload"`
}
type Manifest struct {
	HostAPI      string `json:"host_api"`
	ABI          string `json:"wasm_abi"`
	UI           string `json:"ui_api"`
	Contribution string `json:"contribution"`
	Query        string `json:"query"`
	Command      string `json:"command"`
}
type Registration struct {
	Manifest Manifest
	Module   []byte
}
type Dispatcher struct {
	host          extensions.Runtime
	authorize     Authorize
	registrations map[string]Registration
}

func New(host extensions.Runtime, authorize Authorize, registrations map[string]Registration) (*Dispatcher, error) {
	if host == nil || authorize == nil || host.Status().State != "ready" {
		return nil, extensions.ErrIsolation
	}
	copied := make(map[string]Registration)
	for id, item := range registrations {
		m := item.Manifest
		if id == "" || m.HostAPI != "space.plugin.p1" || m.ABI != "space_plugin_p1" || m.UI != "space.ui.p1" {
			return nil, ErrVersion
		}
		if m.Contribution != "overview" || m.Query != "overview" || m.Command != "acknowledge" {
			return nil, ErrRequest
		}
		if len(item.Module) == 0 || len(item.Module) > extensions.MaxModule {
			return nil, ErrRequest
		}
		item.Module = bytes.Clone(item.Module)
		copied[id] = item
	}
	return &Dispatcher{host, authorize, copied}, nil
}

func Decode(raw []byte) (Request, error) {
	var request Request
	if extensions.CheckJSON(raw) != nil {
		return request, ErrRequest
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return request, ErrRequest
	}
	for _, key := range []string{"protocol", "instance", "kind", "name", "payload"} {
		if _, exists := fields[key]; !exists {
			return request, ErrRequest
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Payload == nil || len(request.Payload) != 0 {
		return request, ErrRequest
	}
	if request.Protocol != Protocol {
		return request, ErrVersion
	}
	if request.Instance == "" || len(request.Instance) > 64 {
		return request, ErrRequest
	}
	return request, nil
}

func (d *Dispatcher) Dispatch(ctx context.Context, credential string, request Request) ([]byte, error) {
	if request.Protocol != Protocol {
		return nil, ErrVersion
	}
	if request.Payload == nil || len(request.Payload) != 0 {
		return nil, ErrRequest
	}
	registration, ok := d.registrations[request.Instance]
	if !ok {
		return nil, ErrDenied
	}
	if !allowed(request, registration.Manifest) {
		return nil, ErrRequest
	}
	principal, err := d.authorize(ctx, credential, request.Instance, request.Kind == "command")
	if err != nil {
		return nil, ErrDenied
	}
	if !validPrincipal(principal, request.Instance) {
		return nil, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, extensions.ProcessTimeout)
	defer cancel()
	input := envelope(ctx, request, principal)
	output, err := d.host.Invoke(ctx, extensions.Invocation{Module: registration.Module, Input: input})
	if err != nil {
		return nil, executionError(err)
	}
	current, err := d.authorize(ctx, credential, request.Instance, request.Kind == "command")
	if err != nil || current != principal {
		return nil, ErrDenied
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if extensions.CheckJSON(output) != nil {
		return nil, ErrExtension
	}
	var document struct {
		UI string `json:"ui_api"`
	}
	if json.Unmarshal(output, &document) != nil || document.UI != registration.Manifest.UI {
		return nil, ErrVersion
	}
	return output, nil
}

func allowed(r Request, m Manifest) bool {
	return (r.Kind == "query" && r.Name == m.Query) || (r.Kind == "command" && r.Name == m.Command)
}

func executionError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, extensions.ErrQuota, extensions.ErrIsolation} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrExtension
}

func validPrincipal(p Principal, instance string) bool {
	revision, err := strconv.ParseUint(p.Revision, 10, 64)
	return p.Instance == instance && p.Space != "" && p.User != "" && p.Device != "" && err == nil && revision > 0 && strconv.FormatUint(revision, 10) == p.Revision
}
