package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorRejectsAmbiguousOrUntrustedInvocation(t *testing.T) {
	for _, arguments := range [][]string{
		{},
		{"-endpoint", "https://example.org:443"},
		{"-home-data", "unused", "-endpoint", "https://example.org:443"},
		{"-home-data", "unused", "unexpected"},
		{"-endpoint", "http://example.org:443"},
	} {
		var output bytes.Buffer
		if err := runDoctor(arguments, &output); err == nil {
			t.Fatalf("Приняты небезопасные параметры: %v", arguments)
		}
	}
}

func TestDoctorLimitsEndpointCountBeforeProbing(t *testing.T) {
	arguments := []string{"-fingerprint", strings.Repeat("0", 64), "-server-id", "srv_" + strings.Repeat("1", 32)}
	for range 9 {
		arguments = append(arguments, "-endpoint", "https://example.org:443")
	}
	var output bytes.Buffer
	if err := runDoctor(arguments, &output); err == nil {
		t.Fatal("Принято более восьми адресов")
	}
}
