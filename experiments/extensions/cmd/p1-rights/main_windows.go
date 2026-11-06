//go:build windows

// Только CI: прямые обращения к ОС из недоверенного worker.
package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tokenFlag(class uint32) uint32 {
	var value, size uint32
	if windows.GetTokenInformation(windows.GetCurrentProcessToken(), class, (*byte)(unsafe.Pointer(&value)), 4, &size) != nil {
		os.Exit(5)
	}
	return value
}

func main() {
	var wire struct{ Request []byte }
	var request struct{ File, Directory, Address string }
	if json.NewDecoder(os.Stdin).Decode(&wire) != nil || json.Unmarshal(wire.Request, &request) != nil {
		os.Exit(2)
	}
	_, readErr := os.ReadFile(request.File)
	writeErr := os.WriteFile(request.File, []byte("tampered"), 0600)
	createErr := os.WriteFile(request.Directory+"\\created", []byte("tampered"), 0600)
	connection, networkErr := net.DialTimeout("tcp", request.Address, time.Second)
	if connection != nil {
		connection.Close()
	}
	result := map[string]any{
		"app_container": tokenFlag(29), "lpac": tokenFlag(46),
		"read_denied":    errors.Is(readErr, os.ErrPermission),
		"write_denied":   errors.Is(writeErr, os.ErrPermission),
		"create_denied":  errors.Is(createErr, os.ErrPermission),
		"network_denied": errors.Is(networkErr, windows.WSAEACCES),
	}
	if networkErr != nil {
		result["network_error"] = networkErr.Error()
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
}
