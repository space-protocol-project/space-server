//go:build darwin

package extensions

import (
	"errors"
	"os"
	"testing"
)

func TestDarwinIsolationRejectsValidPaths(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewProcessHost(t.TempDir(), executable)
	if h != nil || !errors.Is(err, ErrIsolation) {
		t.Fatal("Непроверенная граница macOS не должна разрешать запуск", err)
	}
}
