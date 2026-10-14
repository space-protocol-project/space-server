package home

import (
	"errors"
	"os"
	"path/filepath"
)

func checkPortableSize(staging string) error {
	var total int64
	for _, name := range append(append([]string{}, portableNames...), "space-export.json") {
		info, err := os.Stat(filepath.Join(staging, name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("Экспорт содержит внешний файл")
		}
		total += info.Size()
		if total > maxPortableBytes {
			return errors.New("Экспорт превышает 2 ГиБ")
		}
	}
	return nil
}
