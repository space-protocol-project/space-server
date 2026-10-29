"""Синтетические WAT fixtures; компиляция только в CI через wat2wasm."""
from pathlib import Path
import subprocess

root = Path(__file__).with_name('testdata')
root.mkdir(exist_ok=True)
echo = '(memory.copy (i32.const 65536) (local.get 0) (local.get 1)) (i64.or (i64.const 281474976710656) (i64.extend_i32_u (local.get 1)))'
template = '''(module {imports}
 (memory (export "memory") 2)
 {extra}
 (func (export "space_alloc") (param i32) (result i32) {allocate})
 (func (export "space_free") (param i32 i32) {free})
 (func (export "space_handle") (param i32 i32) (result i64) {handle})
)'''
wait = '(import "space_host_p1" "wait" (func $wait (param i32) (result i32)))'
cases = {
 'echo': {},
 'loop': {'handle': '(loop $l (br $l)) (i64.const 0)'},
 'alloc_loop': {'allocate': '(loop $l (br $l)) (i32.const 1024)'},
 'free_loop': {'free': '(loop $l (br $l))'},
 'start_loop': {'extra': '(func $start (loop $l (br $l))) (start $start)'},
 'pointer': {'handle': '(i64.const 0xfffffff000000040)'},
 'length': {'handle': '(i64.const 0x0001000000010000)'},
 'overlap': {'handle': '(i64.or (i64.shl (i64.extend_i32_u (local.get 0)) (i64.const 32)) (i64.extend_i32_u (local.get 1)))'},
 'grow': {'handle': '(if (i32.ne (memory.grow (i32.const 100)) (i32.const -1)) (then unreachable)) '+echo},
 'state': {'extra': '(global $counter (mut i32) (i32.const 0))', 'handle': '(global.set $counter (i32.add (global.get $counter) (i32.const 1))) (if (i32.ne (global.get $counter) (i32.const 1)) (then unreachable)) '+echo},
 'host_wait': {'imports': wait, 'handle': '(drop (call $wait (i32.const 1000))) '+echo},
 'start_denied': {'imports': wait, 'extra': '(func $start (if (i32.ne (call $wait (i32.const 0)) (i32.const 1)) (then unreachable))) (start $start)'},
 'host_denied': {'imports': wait, 'handle': '(if (i32.ne (call $wait (i32.const 0)) (i32.const 1)) (then unreachable)) '+echo},
 'unknown_import': {'imports': '(import "wasi_snapshot_preview1" "fd_write" (func))'},
 'bad_json': {'extra': '(data (i32.const 65536) "{\\22a\\22:1,\\22a\\22:2}")', 'handle': '(i64.const 0x000100000000000d)'},
}
for name, changes in cases.items():
    values = dict(imports='', extra='', allocate='(i32.const 1024)', free='', handle=echo)
    values.update(changes)
    path = root / f'{name}.wat'
    path.write_text(template.format(**values), encoding='utf-8')
    subprocess.run(['wat2wasm', str(path), '-o', str(path.with_suffix('.wasm'))], check=True)
