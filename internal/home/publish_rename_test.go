package home

import (
	"errors"
	"os"
	"testing"
)

func TestPublishedRenameTransientPermission(t *testing.T) {
	attempts, pauses := 0, 0
	err := retryPublishedRename(func(source, destination string) error {
		if source != "staging" || destination != "destination" {
			t.Fatal(source, destination)
		}
		attempts++
		if attempts < 3 {
			return &os.LinkError{Op: "rename", Old: source, New: destination, Err: os.ErrPermission}
		}
		return nil
	}, "staging", "destination", 5, func() { pauses++ })
	if err != nil || attempts != 3 || pauses != 2 {
		t.Fatal(err, attempts, pauses)
	}
}

func TestPublishedRenameDoesNotHideFailure(t *testing.T) {
	for _, test := range []struct {
		err      error
		attempts int
	}{{os.ErrNotExist, 1}, {os.ErrExist, 1}, {os.ErrPermission, 3}} {
		attempts, pauses := 0, 0
		err := retryPublishedRename(func(string, string) error { attempts++; return test.err }, "staging", "destination", 3, func() { pauses++ })
		if !errors.Is(err, test.err) || attempts != test.attempts || pauses != attempts-1 {
			t.Fatal(err, attempts, pauses)
		}
	}
}
