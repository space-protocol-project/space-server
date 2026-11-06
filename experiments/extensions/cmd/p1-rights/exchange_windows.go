//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func pipeExchangeProbe(raw []byte) bool {
	var request struct{ Mode, Pipe string }
	if json.Unmarshal(raw, &request) != nil || request.Mode != "pipe-exchange" {
		return false
	}
	err := exchangeNamedPipe(request.Pipe)
	if json.NewEncoder(os.Stdout).Encode(resultOf(err)) != nil {
		os.Exit(3)
	}
	return true
}

func exchangeNamedPipe(name string) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	access := uint32(windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.SYNCHRONIZE)
	handle, err := windows.CreateFile(pointer, access, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var count uint32
	if err := windows.WriteFile(handle, []byte("ping"), &count, nil); err != nil {
		return err
	}
	if count != 4 {
		return fmt.Errorf("Неполный запрос")
	}
	var reply [4]byte
	if err := windows.ReadFile(handle, reply[:], &count, nil); err != nil {
		return err
	}
	if count != 4 || string(reply[:]) != "pong" {
		return fmt.Errorf("Неверный ответ")
	}
	return nil
}
