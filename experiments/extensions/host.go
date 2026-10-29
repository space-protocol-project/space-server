// Package extensions проверяет экспериментальный ABI; рабочий сервер его не импортирует.
package extensions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const MaxJSON = 16 * 1024
const MaxModule = 64 * 1024
const MemoryPages = 4
const MaxCalls = 2
const MaxCompiled = 16

var ErrQuota = errors.New("extension quota")
var ErrABI = errors.New("extension ABI")

type grantKey struct{}

// Host не хранит principal: право вызова передаёт доверенный вызывающий код.
type Host struct {
	runtime  wazero.Runtime
	slots    chan struct{}
	mu       sync.Mutex
	compiled map[[32]byte]wazero.CompiledModule
}

func New(ctx context.Context, interpreter bool) (*Host, error) {
	config := wazero.NewRuntimeConfigCompiler()
	if interpreter {
		config = wazero.NewRuntimeConfigInterpreter()
	}
	config = config.WithMemoryLimitPages(MemoryPages).WithCloseOnContextDone(true)
	h := &Host{runtime: wazero.NewRuntimeWithConfig(ctx, config), slots: make(chan struct{}, MaxCalls), compiled: make(map[[32]byte]wazero.CompiledModule)}
	_, err := h.runtime.NewHostModuleBuilder("space_host_p1").NewFunctionBuilder().WithFunc(wait).Export("wait").Instantiate(ctx)
	if err != nil {
		_ = h.Close(ctx)
		return nil, err
	}
	return h, nil
}

// wait — только диагностический host call; не прикладное разрешение.
func wait(ctx context.Context, milliseconds uint32) uint32 {
	if ctx.Value(grantKey{}) != true {
		return 1
	}
	if milliseconds > 1000 {
		return 3
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 2
	case <-timer.C:
		return 0
	}
}

func (h *Host) Close(ctx context.Context) error { return h.runtime.Close(ctx) }

// Load ограничивает байты и число компиляций. CPU компилятора ещё требует отдельной границы процесса.
func (h *Host) Load(ctx context.Context, wasm []byte) (wazero.CompiledModule, error) {
	if len(wasm) == 0 || len(wasm) > MaxModule {
		return nil, ErrQuota
	}
	digest := sha256.Sum256(wasm)
	h.mu.Lock()
	defer h.mu.Unlock()
	if module := h.compiled[digest]; module != nil {
		return module, nil
	}
	if len(h.compiled) >= MaxCompiled {
		return nil, ErrQuota
	}
	module, err := h.runtime.CompileModule(ctx, wasm)
	if err != nil {
		return nil, err
	}
	if err = checkABI(module); err != nil {
		_ = module.Close(ctx)
		return nil, err
	}
	h.compiled[digest] = module
	return module, nil
}

func checkABI(module wazero.CompiledModule) error {
	if len(module.ImportedMemories()) != 0 || len(module.ExportedMemories()) != 1 || module.ExportedMemories()["memory"] == nil {
		return ErrABI
	}
	for _, f := range module.ImportedFunctions() {
		namespace, name, _ := f.Import()
		if namespace != "space_host_p1" || name != "wait" || !signature(f, []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}) {
			return ErrABI
		}
	}
	functions := module.ExportedFunctions()
	if !signature(functions["space_alloc"], []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}) {
		return ErrABI
	}
	if !signature(functions["space_free"], []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, nil) {
		return ErrABI
	}
	if !signature(functions["space_handle"], []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}) {
		return ErrABI
	}
	return nil
}

func signature(f api.FunctionDefinition, parameters, results []api.ValueType) bool {
	if f == nil {
		return false
	}
	return fmt.Sprint(f.ParamTypes()) == fmt.Sprint(parameters) && fmt.Sprint(f.ResultTypes()) == fmt.Sprint(results)
}

func (h *Host) Call(ctx context.Context, module wazero.CompiledModule, request []byte, allowWait bool) ([]byte, error) {
	if err := CheckJSON(request); err != nil {
		return nil, err
	}
	select {
	case h.slots <- struct{}{}:
	default:
		return nil, ErrQuota
	}
	defer func() { <-h.slots }()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	// Core start исполняется с тем же deadline, но без полномочий host call.
	instance, err := h.runtime.InstantiateModule(context.WithValue(ctx, grantKey{}, false), module, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer instance.Close(context.Background())
	return exchange(context.WithValue(ctx, grantKey{}, allowWait), instance, request)
}

func exchange(ctx context.Context, module api.Module, request []byte) ([]byte, error) {
	allocated, err := module.ExportedFunction("space_alloc").Call(ctx, uint64(len(request)))
	if err != nil {
		return nil, err
	}
	input := uint32(allocated[0])
	if !module.Memory().Write(input, request) {
		return nil, ErrABI
	}
	packed, err := module.ExportedFunction("space_handle").Call(ctx, uint64(input), uint64(len(request)))
	if err != nil {
		return nil, err
	}
	pointer, length := uint32(packed[0]>>32), uint32(packed[0])
	if length == 0 || length > MaxJSON {
		return nil, ErrQuota
	}
	if uint64(pointer) < uint64(input)+uint64(len(request)) && uint64(input) < uint64(pointer)+uint64(length) {
		return nil, ErrABI
	}
	result, ok := module.Memory().Read(pointer, length)
	if !ok {
		return nil, ErrABI
	}
	copied := append([]byte(nil), result...)
	if err = CheckJSON(copied); err != nil {
		return nil, err
	}
	for _, region := range [][2]uint64{{uint64(pointer), uint64(length)}, {uint64(input), uint64(len(request))}} {
		if _, err = module.ExportedFunction("space_free").Call(ctx, region[0], region[1]); err != nil {
			return nil, err
		}
	}
	return copied, nil
}
