"""Генерация демонстрационного WAT; только CI, без SDK для сторонних авторов."""
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1] / "testdata"
root.mkdir(exist_ok=True)

def document(message):
    return json.dumps({"ui_api":"space.ui.p1", "strings": {
        "en":{"title":"Demo extension", "status":message[0], "action":"Run command"},
        "ru":{"title":"Демонстрационное расширение", "status":message[1], "action":"Выполнить команду"}},
        "body":[{"id":"demo", "component":"section.v1", "label_key":"title", "children":[
            {"id":"status", "component":"text.v1", "label_key":"status"},
            {"id":"action", "component":"button.v1", "label_key":"action", "command":"acknowledge"}]}]}, ensure_ascii=False, separators=(",",":")).encode()

def literal(raw):
    return '"' + ''.join('\\%02x' % byte for byte in raw) + '"'

query = b'{"abi":"space_plugin_p1","kind":"query","name":"overview",'
command = b'{"abi":"space_plugin_p1","kind":"command","name":"acknowledge",'
initial = document(("Ready", "Готово к проверке"))
result = document(("Command completed", "Команда выполнена"))
wat = f'''(module
 (memory (export "memory") 2)
 (data (i32.const 32768) {literal(query)})
 (data (i32.const 33024) {literal(command)})
 (data (i32.const 65536) {literal(initial)})
 (data (i32.const 81920) {literal(result)})
 (func (export "space_alloc") (param i32) (result i32) (i32.const 1024))
 (func (export "space_free") (param i32 i32))
 (func $prefix (param $p i32) (param $n i32) (param $q i32) (param $len i32) (result i32)
  (local $i i32)
  (if (i32.lt_u (local.get $n) (local.get $len)) (then (return (i32.const 0))))
  (loop $next
   (if (i32.ne (i32.load8_u (i32.add (local.get $p) (local.get $i))) (i32.load8_u (i32.add (local.get $q) (local.get $i)))) (then (return (i32.const 0))))
   (local.set $i (i32.add (local.get $i) (i32.const 1)))
   (br_if $next (i32.lt_u (local.get $i) (local.get $len))))
  (i32.const 1))
 (func (export "space_handle") (param $p i32) (param $n i32) (result i64)
  (if (call $prefix (local.get $p) (local.get $n) (i32.const 32768) (i32.const {len(query)}))
   (then (return (i64.const {(65536<<32)|len(initial)}))))
  (if (call $prefix (local.get $p) (local.get $n) (i32.const 33024) (i32.const {len(command)}))
   (then (return (i64.const {(81920<<32)|len(result)}))))
  unreachable))'''
(root / "demo.wat").write_text(wat)
subprocess.run(["wat2wasm", str(root / "demo.wat"), "-o", str(root / "demo.wasm")], check=True)
