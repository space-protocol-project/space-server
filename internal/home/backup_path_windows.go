package home

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Путь через handle раскрывает также junction и SUBST: разные буквы дисков
// не должны позволять сохранить копию внутрь исходного каталога.
func finalBackupPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]uint16, 32768)
	size, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if size >= uint32(len(buffer)) {
		return "", errors.New("Слишком длинный путь резервной копии")
	}
	resolved := windows.UTF16ToString(buffer[:size])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}
