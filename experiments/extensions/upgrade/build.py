"""Детерминированные WASM-миграции и аварийные варианты для GitHub CI."""
import json
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1] / 'testdata'
root.mkdir(exist_ok=True)
def literal(raw):
    return '"' + ''.join('\\%02x' % byte for byte in raw) + '"'
seed = json.dumps({'version':'space.migration.data.p1','schema':1,'values':{'note':{'value':1}}},separators=(',',':')).encode()
(root / 'migration-data.json').write_bytes(seed)
output = json.dumps({'version':'space.migration.data.p1','schema':2,'values':{'added':True,'note':{'value':2}}},separators=(',',':')).encode()
for kind in ('ok','trap','loop','invalid','schema','memory','next','empty','empty-next'):
    current_seed = seed
    result = output
    if kind == 'empty':
        current_seed = json.dumps({'version':'space.migration.data.p1','schema':1,'values':{}},separators=(',',':')).encode()
        result = json.dumps({'version':'space.migration.data.p1','schema':2,'values':{'other':True}},separators=(',',':')).encode()
    if kind == 'empty-next':
        current_seed = json.dumps({'version':'space.migration.data.p1','schema':2,'values':{'other':True}},separators=(',',':')).encode()
        result = json.dumps({'version':'space.migration.data.p1','schema':3,'values':{'other':True,'schema3':True}},separators=(',',':')).encode()
    if kind == 'next':
        current_seed = output
        result = json.dumps({'version':'space.migration.data.p1','schema':3,'values':{'added':True,'note':{'value':3},'schema3':True}},separators=(',',':')).encode()
    if kind == 'invalid': result = b'{"version":"bad"}'
    if kind == 'schema': result = seed
    operation = f'(i64.const {(65536<<32)|len(result)})'
    if kind == 'trap': operation = 'unreachable'
    if kind == 'loop': operation = '(loop $forever (br $forever)) unreachable'
    if kind == 'memory': operation = '(drop (memory.grow (i32.const 100))) unreachable'
    wat = f'''(module
 (memory (export "memory") 2)
 (data (i32.const 32768) {literal(current_seed)})
 (data (i32.const 65536) {literal(result)})
 (func (export "space_alloc") (param i32) (result i32) (i32.const 1024))
 (func (export "space_free") (param i32 i32))
 (func (export "space_handle") (param $p i32) (param $n i32) (result i64)
  (local $i i32)
  (if (i32.ne (local.get $n) (i32.const {len(current_seed)})) (then unreachable))
  (loop $check
   (if (i32.ne (i32.load8_u (i32.add (local.get $p) (local.get $i))) (i32.load8_u (i32.add (i32.const 32768) (local.get $i)))) (then unreachable))
   (local.set $i (i32.add (local.get $i) (i32.const 1)))
   (br_if $check (i32.lt_u (local.get $i) (local.get $n))))
  {operation}))'''
    path = root / f'migrate-{kind}.wat'
    path.write_text(wat, encoding='utf-8')
    subprocess.run(['wat2wasm', str(path), '-o', str(path.with_suffix('.wasm'))],check=True)
