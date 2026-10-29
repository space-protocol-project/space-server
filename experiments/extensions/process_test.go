package extensions

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestProcessZeroValueRejectsLaunch(t *testing.T) {
	h := &ProcessHost{}
	_, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`{}`), false, false)
	if !errors.Is(err, ErrIsolation) {
		t.Fatal(err)
	}
}

func TestProcessUnavailable(t *testing.T) {
	h, err := NewProcessHost(t.TempDir(), "/not-a-worker")
	if h != nil || !errors.Is(err, ErrIsolation) {
		t.Fatal("Нельзя запускать без изоляции", h, err)
	}
	t.Logf("platform=%s: fail closed for unavailable launch configuration", runtime.GOOS)
}

func TestProcessOutputBound(t *testing.T) {
	output := &cappedOutput{limit: 4}
	if _, err := output.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("5")); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	if len(output.data) != 4 || !output.exceeded {
		t.Fatal("Буфер превысил лимит")
	}
}
