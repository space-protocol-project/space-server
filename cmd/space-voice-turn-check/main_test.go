package main

import "testing"

func TestRejectsNonPrivateTurnInterface(t *testing.T) {
	for _, address := range []string{"invalid", "8.8.8.8", "127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			t.Setenv("SPACE_VOICE_TURN_CHECK_IP", address)
			if _, err := privateAddress(); err == nil {
				t.Fatal("Разрешён недопустимый интерфейс стенда")
			}
		})
	}
}
