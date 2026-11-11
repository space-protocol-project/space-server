//go:build windows

// Только CI: прямые обращения к ОС из недоверенного worker.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/internal/wintoken"
	"golang.org/x/sys/windows"
)

func tokenFlag(class uint32) uint32 {
	value, err := wintoken.Flag(windows.GetCurrentProcessToken(), class)
	if err != nil {
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
	if resourceProbe(wire.Request) {
		return
	}
	if profileIPCProbe(wire.Request) {
		return
	}
	if pipeExchangeProbe(wire.Request) {
		return
	}
	if pipeRevokeProbe(wire.Request) {
		return
	}
	if networkProbe(wire.Request) {
		return
	}
	if profileDiskProbe(wire.Request) {
		return
	}
	if boundaryProbe(wire.Request) {
		return
	}
	_, readErr := os.ReadFile(request.File)
	writeErr := os.WriteFile(request.File, []byte("tampered"), 0600)
	createErr := os.WriteFile(request.Directory+"\\created", []byte("tampered"), 0600)
	var winsock windows.WSAData
	startupErr := windows.WSAStartup(0x202, &winsock)
	if startupErr == nil {
		defer windows.WSACleanup()
	}
	connection, networkErr := net.DialTimeout("tcp", request.Address, time.Second)
	if connection != nil {
		connection.Close()
	}
	result := map[string]any{
		"app_container":     tokenFlag(29),
		"read_denied":       errors.Is(readErr, os.ErrPermission),
		"write_denied":      errors.Is(writeErr, os.ErrPermission),
		"create_denied":     errors.Is(createErr, os.ErrPermission),
		"network_denied":    errors.Is(networkErr, windows.WSAEACCES),
		"network_connected": networkErr == nil,
		"winsock_startup":   fmt.Sprint(startupErr),
	}
	if networkErr != nil {
		result["network_error"] = networkErr.Error()
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
}
