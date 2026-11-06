//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Только CI: идентичность объекта исключает ложный успех при повторном
// использовании числового значения handle другим объектом worker.
func boundaryProbe(raw []byte) bool {
	var request struct {
		Mode, File, Directory string
		Handle                uintptr
		Volume, High, Low     uint32
	}
	if json.Unmarshal(raw, &request) != nil {
		return false
	}
	var result any
	switch request.Mode {
	case "handle":
		var info windows.ByHandleFileInformation
		err := windows.GetFileInformationByHandle(windows.Handle(request.Handle), &info)
		result = map[string]any{
			"query":     resultOf(err),
			"same_file": err == nil && info.VolumeSerialNumber == request.Volume && info.FileIndexHigh == request.High && info.FileIndexLow == request.Low,
		}
	case "files":
		result = map[string]accessResult{
			"read":   resultOf(nativeFile(request.File, false, windows.OPEN_EXISTING)),
			"write":  resultOf(nativeFile(request.File, true, windows.OPEN_EXISTING)),
			"create": resultOf(nativeFile(filepath.Join(request.Directory, "created"), true, windows.CREATE_NEW)),
		}
	default:
		return false
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
	return true
}

func nativeFile(path string, write bool, disposition uint32) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	access := uint32(windows.GENERIC_READ)
	if write {
		access = windows.GENERIC_WRITE
	}
	handle, err := windows.CreateFile(pointer, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var count uint32
	if write {
		data := []byte("tampered")
		if err := windows.WriteFile(handle, data, &count, nil); err != nil {
			return err
		}
		if count != uint32(len(data)) {
			return fmt.Errorf("Неполная запись")
		}
		return nil
	}
	var data [16]byte
	if err := windows.ReadFile(handle, data[:], &count, nil); err != nil {
		return err
	}
	if string(data[:count]) != "secret" {
		return fmt.Errorf("Неожиданное содержимое")
	}
	return nil
}
