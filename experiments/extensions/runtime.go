package extensions

import "context"

// Runtime — внутренний контракт P1; запуск выполняет NewProcessHost.
// Вызовы принимаются только в ready; Close переводит хост в draining до очистки.
type Runtime interface {
	Invoke(context.Context, Invocation) ([]byte, error)
	Status() RuntimeStatus
	Close() error
}

type Invocation struct{ Module, Input []byte }
type RuntimeStatus struct {
	State                   string
	WorkerBytes, TotalBytes uint64
	MaxConcurrent           int
}

func (h *ProcessHost) Invoke(ctx context.Context, call Invocation) ([]byte, error) {
	return h.CompileAndCall(ctx, call.Module, call.Input, false, false)
}

func (h *ProcessHost) Status() RuntimeStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := "ready"
	if h.closed {
		state = "draining"
	}
	if !h.ready {
		state = "stopped"
	}
	return RuntimeStatus{state, WorkerMemory, TotalMemory, MaxCalls}
}

var _ Runtime = (*ProcessHost)(nil)
