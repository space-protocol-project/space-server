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
		data, err := os.ReadFile(request.File)
		if err == nil && string(data) != "secret" {
			err = fmt.Errorf("Неожиданное содержимое")
		}
		result = map[string]accessResult{
			"read":   resultOf(err),
			"write":  resultOf(os.WriteFile(request.File, []byte("tampered"), 0600)),
			"create": resultOf(os.WriteFile(filepath.Join(request.Directory, "created"), []byte("tampered"), 0600)),
		}
	default:
		return false
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
	return true
}
