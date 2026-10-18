//go:build !linux && !darwin && !windows

package home

import "errors"

func scheduledFreeBytes(string) (uint64, error) {
	return 0, errors.New("Платформа не поддерживается")
}
