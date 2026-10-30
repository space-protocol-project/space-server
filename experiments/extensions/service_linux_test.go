//go:build linux

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Только стенд systemd: барьеры аварии не входят в p1-supervisor.
func TestServiceHelper(t *testing.T) {
	control := os.Getenv("P1_SERVICE_CONTROL")
	if control == "" {
		t.Skip("Требуется отдельный systemd CI")
	}
	gate, err := os.OpenFile(filepath.Join(control, "once"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	first := err == nil
	if first {
		gate.Close()
	} else if !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	stage := os.Getenv("P1_SERVICE_STAGE")
	if first && stage == "before-host" {
		serviceMarker(t, control, stage, "", false)
		time.Sleep(time.Minute)
		return
	}
	h, err := serviceHost(os.Getenv("P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if first && stage == "before-ready" {
		serviceMarker(t, control, stage, h.group, false)
		time.Sleep(time.Minute)
		return
	}
	healthy(t, h)
	if err := serviceReady(); err != nil {
		t.Fatal(err)
	}
	if first && stage == "running" {
		servicePressure(t, h, control)
		return
	}
	emptyGroups(t, h)
	serviceMarker(t, control, "recovered", h.group, true)
	time.Sleep(time.Minute)
}

func servicePressure(t *testing.T, h *ProcessHost, control string) {
	h.executable = os.Getenv("P1_PRESSURE")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
	done := make(chan error, 1)
	go func() { _, err := h.run(ctx, input); done <- err }()
	waitRecoveryProcesses(t, h.group)
	serviceMarker(t, control, "running", h.group, false)
	<-done
}

func serviceMarker(t *testing.T, control, phase, group string, healthy bool) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"pid": os.Getpid(), "phase": phase, "group": group, "healthy": healthy})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(control, "status.json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}
