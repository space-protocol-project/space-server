package demo

import (
	"context"
	"encoding/json"
	"errors"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"strings"
	"testing"
)

type fakeHost struct {
	calls int
	input []byte
	err   error
	after func()
}

func (h *fakeHost) Status() extensions.RuntimeStatus { return extensions.RuntimeStatus{State: "ready"} }
func (h *fakeHost) Close() error                     { return nil }
func (h *fakeHost) Invoke(ctx context.Context, call extensions.Invocation) ([]byte, error) {
	h.calls++
	h.input = call.Input
	if h.after != nil {
		h.after()
	}
	return []byte(`{"ui_api":"space.ui.p1"}`), h.err
}
func setup(t *testing.T, h *fakeHost, a Authorize) *Dispatcher {
	t.Helper()
	d, err := New(h, a, map[string]Registration{"demo": {Manifest: Manifest{"space.plugin.p1", "space_plugin_p1", "space.ui.p1", "overview", "overview", "acknowledge"}, Module: []byte("fixture")}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func request() Request {
	return Request{Protocol, "demo", "query", "overview", map[string]json.RawMessage{}}
}
func grant(context.Context, string, string, bool) (Principal, error) {
	return Principal{"space_demo", "demo", "user", "device", "1"}, nil
}

func TestDecodeRejectsUntrustedContext(t *testing.T) {
	for _, raw := range []string{
		`{"Protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":{}}`,
		`{"protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":{},"context":{}}`,
		`{"protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":{"principal_id":"admin"}}`,
		`{"protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":null}`,
		`{"protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":{},"name":"acknowledge"}`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestAccessBeforeWASM(t *testing.T) {
	h := &fakeHost{}
	d := setup(t, h, func(context.Context, string, string, bool) (Principal, error) { return Principal{}, ErrDenied })
	if _, err := d.Dispatch(context.Background(), "", request()); !errors.Is(err, ErrDenied) || h.calls != 0 {
		t.Fatal(err, h.calls)
	}
}
func TestTrustedEnvelope(t *testing.T) {
	h := &fakeHost{}
	d := setup(t, h, grant)
	if _, err := d.Dispatch(context.Background(), "", request()); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Context trustedContext `json:"context"`
	}
	if json.Unmarshal(h.input, &value) != nil || value.Context.User != "user" || value.Context.Instance != "demo" || value.Context.Deadline == "" {
		t.Fatal(string(h.input))
	}
}
func TestRevokeDuringCall(t *testing.T) {
	revision := "1"
	h := &fakeHost{after: func() { revision = "2" }}
	d := setup(t, h, func(context.Context, string, string, bool) (Principal, error) {
		p, _ := grant(context.Background(), "", "", false)
		p.Revision = revision
		return p, nil
	})
	if _, err := d.Dispatch(context.Background(), "", request()); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestStableFailureAndRecovery(t *testing.T) {
	h := &fakeHost{err: errors.New("private compiler details")}
	d := setup(t, h, grant)
	if _, err := d.Dispatch(context.Background(), "", request()); !errors.Is(err, ErrExtension) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	h.err = nil
	if _, err := d.Dispatch(context.Background(), "", request()); err != nil {
		t.Fatal(err)
	}
}
func TestVersionAndCommandBeforeWASM(t *testing.T) {
	h := &fakeHost{}
	d := setup(t, h, grant)
	r := request()
	r.Protocol = "v99"
	if _, err := d.Dispatch(context.Background(), "", r); !errors.Is(err, ErrVersion) {
		t.Fatal(err)
	}
	r = request()
	r.Name = "grant_admin"
	if _, err := d.Dispatch(context.Background(), "", r); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	if h.calls != 0 {
		t.Fatal(h.calls)
	}
}

func TestRejectInvalidAccessRevision(t *testing.T) {
	for _, revision := range []string{"0", "01", "-1", "18446744073709551616"} {
		h := &fakeHost{}
		d := setup(t, h, func(context.Context, string, string, bool) (Principal, error) {
			p, _ := grant(context.Background(), "", "", false)
			p.Revision = revision
			return p, nil
		})
		if _, err := d.Dispatch(context.Background(), "", request()); !errors.Is(err, ErrDenied) || h.calls != 0 {
			t.Fatal(revision, err, h.calls)
		}
	}
}
