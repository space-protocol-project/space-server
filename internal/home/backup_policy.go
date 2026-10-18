package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type BackupPolicy struct {
	Format        int    `json:"format"`
	Enabled       bool   `json:"enabled"`
	Prune         bool   `json:"prune"`
	IntervalHours int    `json:"intervalHours"`
	KeepCount     int    `json:"keepCount"`
	KeepDays      int    `json:"keepDays"`
	MaxBytes      uint64 `json:"maxBytes"`
}

func (p BackupPolicy) Validate() error {
	if p.Format != 1 || p.IntervalHours < 1 || p.IntervalHours > 168 || p.KeepCount < 3 || p.KeepCount > 100 || p.KeepDays < 1 || p.KeepDays > 3650 || p.MaxBytes > 1<<40 {
		return errors.New("Некорректные настройки копирования")
	}
	return nil
}

type BackupStatus struct {
	Format        int    `json:"format"`
	Code          string `json:"code"`
	ServerID      string `json:"serverId"`
	Policy        string `json:"policy"`
	LastSuccessAt int64  `json:"lastSuccessAt"`
	LastAttemptAt int64  `json:"lastAttemptAt"`
	NextAt        int64  `json:"nextAt"`
	Count         int    `json:"count"`
	Bytes         uint64 `json:"bytes"`
	Protected     int    `json:"protected"`
	Limited       bool   `json:"limited"`
}

func readBackupJSON(name string, target any) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return errors.New("Недопустимый файл настроек")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("Лишние данные настроек")
	}
	return nil
}

func writeBackupJSON(name string, target any) error {
	data, err := json.Marshal(target)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".backup-state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := protectBackupDirectory(file.Name()); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), name)
}
