"""Артефакты и обязательные результаты межплатформенного стенда P1."""
import hashlib
import json
from pathlib import Path, PurePosixPath
import sys
import tarfile


def pack_runtime():
    archive = Path('.local/runtime.tar.gz')
    archive.parent.mkdir(exist_ok=True)
    with tarfile.open(archive, 'w:gz', dereference=True) as target:
        target.add('dist/home', arcname='home')
    archive.with_suffix('.sha256').write_text(hashlib.sha256(archive.read_bytes()).hexdigest(), encoding='ascii')


def unpack_runtime():
    archive = Path('.local/runtime/runtime.tar.gz')
    expected = archive.with_suffix('.sha256').read_text(encoding='ascii').strip()
    if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
        raise ValueError('Изменён runtime из задания источника')
    with tarfile.open(archive, 'r:gz') as source:
        for entry in source.getmembers():
            path = PurePosixPath(entry.name)
            if path.is_absolute() or '..' in path.parts or path.parts[0] != 'home' or not (entry.isfile() or entry.isdir()):
                raise ValueError('Недопустимый путь runtime')
        source.extractall('dist', filter='data')


def check_results(kind):
    path = Path('.local/p1-transfer') / (kind + '.jsonl')
    events = [json.loads(line) for line in path.read_text(encoding='utf-8').splitlines()]
    if any(e['Action'] in ('fail', 'skip') for e in events):
        raise ValueError('В целевом прогоне есть ошибки или пропуски')
    if kind == 'export':
        required = {'TestExtensionP1RegistryTransferExport/' + name for name in ('full', 'retired')}
        repeats = 1
    else:
        target = {'win32': 'windows', 'darwin': 'darwin', 'linux': 'linux'}[sys.platform]
        required = {'TestExtensionP1RegistryTransferImport/' + source + '_to_' + target + '/' + name
                    for source in ('linux', 'windows', 'darwin') if source != target
                    for name in ('full', 'retired')}
        repeats = 3
    for name in required:
        if sum(e['Action'] == 'pass' and e.get('Test') == name for e in events) != repeats:
            raise ValueError('Не выполнен обязательный сценарий: ' + name)


if __name__ == '__main__':
    action = sys.argv[1]
    if action == 'pack':
        pack_runtime()
    elif action == 'unpack':
        unpack_runtime()
    elif action in ('export', 'import'):
        check_results(action)
    else:
        raise ValueError('Неизвестное действие')
