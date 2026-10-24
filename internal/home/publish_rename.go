package home

import (
	"os"
	"runtime"
	"time"
)

func renamePublishedDirectory(source, destination string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 41
	}
	return retryPublishedRename(os.Rename, source, destination, attempts, func() { time.Sleep(50 * time.Millisecond) })
}

// После остановки PostgreSQL Windows может ещё удерживать дескрипторы каталога.
// Повторяем только отказ в доступе, максимум две секунды. Действующий каталог
// не удаляется: каждая попытка использует ту же атомарную операцию rename.
func retryPublishedRename(rename func(string, string) error, source, destination string, attempts int, pause func()) error {
	for attempt := 1; ; attempt++ {
		err := rename(source, destination)
		if err == nil || !os.IsPermission(err) || attempt >= attempts {
			return err
		}
		pause()
	}
}
