"""Обязательные результаты архивного стенда истории версий."""
import json
from pathlib import Path
import sys

sys.stdout.reconfigure(encoding='utf-8')
sys.stderr.reconfigure(encoding='utf-8')

kind = sys.argv[1]
events = [json.loads(line) for line in (Path('.local/p1-version-transfer') / (kind + '.jsonl')).read_text(encoding='utf-8').splitlines()]
if any(e['Action'] in ('fail', 'skip') for e in events):
    raise ValueError('Ошибки или пропуски в целевом наборе')
states = ('published', 'pending', 'ready')
prefix = 'TestExtensionP1VersionArchive'
if kind == 'seed':
    names = {prefix + 'Seed/' + s for s in states}
    repeats = 1
elif kind == 'export':
    names = {prefix + 'Export/' + s + '/' + fmt for s in states for fmt in ('physical', 'zip')}
    names |= {prefix + 'Invalid/' + fmt + '/' + defect for fmt in ('physical', 'zip') for defect in ('digest', 'gap', 'module')}
    repeats = 1
elif kind in ('import', 'resume'):
    target = {'linux': 'linux', 'win32': 'windows', 'darwin': 'darwin'}[sys.platform]
    test = 'Import' if kind == 'import' else 'Resume'
    names = {prefix + test + '/' + src + '_to_' + target + '/' + s for src in ('linux', 'windows', 'darwin') if src != target for s in states}
    repeats = 3
else:
    raise ValueError('Неизвестный набор')
for name in names:
    if sum(e['Action'] == 'pass' and e.get('Test') == name for e in events) != repeats:
        raise ValueError('Не выполнен сценарий: ' + name)
print(f'{kind}: {len(names) * repeats} обязательных конечных результатов')
