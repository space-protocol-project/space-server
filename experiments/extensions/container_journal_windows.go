//go:build windows

package extensions

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const containerJournal = "lpac-state.json"

var openContainerJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")

type containerIntent struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	ID      string `json:"id"`
}

func rootDigest(root string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return hex.EncodeToString(digest[:])
}

func plainPath(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrIsolation
	}
	return nil
}

func lockContainerRoot(root string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrIsolation
	}
	if err := plainPath(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "lpac.lock")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err := plainPath(path); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func readIntent(root string) (*containerIntent, error) {
	path := filepath.Join(root, containerJournal)
	if err := plainPath(path); err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 513))
	if err != nil || len(raw) > 512 {
		return nil, ErrIsolation
	}
	var intent containerIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return nil, ErrIsolation
	}
	canonical, err := json.Marshal(intent)
	id, idError := hex.DecodeString(intent.ID)
	if err != nil || !bytes.Equal(raw, canonical) || intent.Version != 1 || intent.Root != rootDigest(root) || idError != nil || len(id) != 16 || hex.EncodeToString(id) != intent.ID {
		return nil, ErrIsolation
	}
	return &intent, nil
}

func intentContainer(root string, intent containerIntent) *appContainer {
	profile := "Space.P1." + intent.Root[:16] + "." + intent.ID
	name, _ := windows.UTF16PtrFromString(profile)
	job, _ := windows.UTF16PtrFromString("Local\\" + profile)
	return &appContainer{name: name, jobName: job, directory: filepath.Join(root, "lpac-"+intent.ID), journal: filepath.Join(root, containerJournal), policy: 1}
}

func beginContainer(root string) (*appContainer, error) {
	lock, err := lockContainerRoot(root)
	if err != nil {
		return nil, err
	}
	c, err := beginLockedContainer(root)
	if err != nil {
		lock.Close()
		return nil, err
	}
	c.lock = lock
	return c, nil
}

func beginLockedContainer(root string) (*appContainer, error) {
	old, err := readIntent(root)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := intentContainer(root, *old).clean(); err != nil {
			return nil, err
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	intent := containerIntent{Version: 1, Root: rootDigest(root), ID: hex.EncodeToString(nonce[:])}
	raw, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, containerJournal), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	return intentContainer(root, intent), nil
}

// Именованный Job позволяет дождаться старого дерева до удаления профиля.
func (c *appContainer) stopRecordedJob() error {
	// JOB_OBJECT_QUERY (0x4) | JOB_OBJECT_TERMINATE (0x8), без наследования.
	handle, _, err := openContainerJob.Call(0x4|0x8, 0, uintptr(unsafe.Pointer(c.jobName)))
	if handle == 0 && errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if handle == 0 {
		return err
	}
	job := windows.Handle(handle)
	defer windows.CloseHandle(job)
	return stopJob(job)
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *appContainer) removeSnapshot() error {
	err := plainPath(c.directory)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	// Не рекурсивное удаление: посторонние файлы блокируют очистку.
	if err := removeIfPresent(filepath.Join(c.directory, "worker.exe")); err != nil {
		return err
	}
	return removeIfPresent(c.directory)
}

func (c *appContainer) clean() error {
	if c.journal == "" {
		return nil
	}
	if err := c.stopRecordedJob(); err != nil {
		return err
	}
	result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(c.name)))
	if int32(result) < 0 {
		return fmt.Errorf("DeleteAppContainerProfile: %#x", result)
	}
	if err := c.removeSnapshot(); err != nil {
		return err
	}
	if err := removeIfPresent(c.journal); err != nil {
		return err
	}
	c.journal = ""
	return nil
}
