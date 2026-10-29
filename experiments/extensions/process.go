package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const WorkerMemory = 256 << 20
const TotalMemory = 512 << 20
const ProcessTimeout = 2 * time.Second
const maxWire = 128 << 10

var ErrIsolation = errors.New("extension process isolation unavailable")
var ErrWorker = errors.New("extension worker failed")

// ProcessHost принадлежит всему серверу, а не отдельному пространству.
// Исполняемый файл и корень cgroup задаёт администратор, не manifest расширения.
type ProcessHost struct {
	executable string
	group      string
	mu         sync.Mutex
	active     int
	closed     bool
	ready      bool
	platform   processPlatform
}

type ProcessStats struct {
	Active        int
	ReservedBytes uint64
	CurrentBytes  uint64
	PeakBytes     uint64
	OOMKills      uint64
	CPUUsec       uint64
	Throttled     uint64
	OSProcesses   uint32
}

type workerRequest struct {
	WASM        []byte `json:"wasm"`
	Request     []byte `json:"request"`
	Interpreter bool   `json:"interpreter"`
	AllowWait   bool   `json:"allow_wait"`
}

func (h *ProcessHost) reserve() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !h.ready || h.executable == "" {
		return ErrIsolation
	}
	if h.active >= MaxCalls || uint64(h.active+1)*WorkerMemory > TotalMemory {
		return ErrQuota
	}
	h.active++
	return nil
}

func (h *ProcessHost) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
}

// CompileAndCall никогда не компилирует WASM в процессе вызывающего кода.
// Квота резервируется до сериализации и запуска, освобождается после reap/очистки.
func (h *ProcessHost) CompileAndCall(ctx context.Context, wasm, request []byte, interpreter, allowWait bool) ([]byte, error) {
	if len(wasm) == 0 || len(wasm) > MaxModule {
		return nil, ErrQuota
	}
	if err := CheckJSON(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := h.reserve(); err != nil {
		return nil, err
	}
	defer h.release()
	input, err := json.Marshal(workerRequest{wasm, request, interpreter, allowWait})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessTimeout)
	defer cancel()
	output, err := h.run(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := CheckJSON(output); err != nil {
		return nil, err
	}
	return output, nil
}

// Worker обслуживает ровно один запрос. Кеш и все данные исчезают с процессом.
func Worker(input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxWire+1))
	if err != nil || len(raw) > maxWire {
		return ErrQuota
	}
	var request workerRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrJSON
	}
	h, err := newLocalHost(context.Background(), request.Interpreter)
	if err != nil {
		return err
	}
	defer h.Close(context.Background())
	module, err := h.Load(context.Background(), request.WASM)
	if err != nil {
		return err
	}
	result, err := h.Call(context.Background(), module, request.Request, request.AllowWait)
	if err != nil {
		return err
	}
	_, err = output.Write(result)
	return err
}

type cappedOutput struct {
	data       []byte
	limit      int
	exceeded   bool
	onOverflow func()
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-len(w.data) {
		w.exceeded = true
		if w.onOverflow != nil {
			w.onOverflow()
		}
		return 0, ErrQuota
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
