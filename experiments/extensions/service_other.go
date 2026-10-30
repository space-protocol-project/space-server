//go:build !linux

package extensions

import "context"

func Service(ctx context.Context, executable string) error { return ErrIsolation }
