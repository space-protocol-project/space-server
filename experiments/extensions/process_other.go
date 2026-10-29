//go:build !linux

package extensions

import "context"

func NewProcessHost(root, executable string) (*ProcessHost, error) { return nil, ErrIsolation }
func (h *ProcessHost) run(ctx context.Context, input []byte) ([]byte, error) {
	return nil, ErrIsolation
}
func (h *ProcessHost) Stats() ProcessStats { return ProcessStats{} }
func (h *ProcessHost) Close() error        { return ErrIsolation }
