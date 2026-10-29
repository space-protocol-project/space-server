"""Сохранить RSS отдельных процессов клиента/runtime при сквозном CI-сценарии."""
import argparse
import json
from pathlib import Path
import subprocess
import time


def sample(root):
    rows = subprocess.check_output(['ps', '-eo', 'pid=,ppid=,rss=,args='], text=True)
    processes = {}
    for line in rows.splitlines():
        values = line.strip().split(None, 3)
        if len(values) == 4:
            pid, parent, rss, command = values
            processes[int(pid)] = (int(parent), int(rss), command)
    descendants = {root}
    for _ in range(len(processes)):
        found = {pid for pid, (parent, _, _) in processes.items() if parent in descendants}
        if found <= descendants:
            break
        descendants |= found
    totals = {'runtime_rss_kib': 0, 'dart_client_rss_kib': 0}
    for pid in descendants:
        if pid not in processes:
            continue
        _, rss, command = processes[pid]
        executable = command.split()[0]
        if Path(executable).name == 'space-server':
            totals['runtime_rss_kib'] += rss
        elif 'dart' in Path(executable).name:
            totals['dart_client_rss_kib'] += rss
    return totals


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[0] == '--' else args.command
    samples = []
    start = time.monotonic()
    process = subprocess.Popen(command)
    try:
        while process.poll() is None:
            samples.append({'seconds': time.monotonic() - start, **sample(process.pid)})
            if time.monotonic() - start > 900:
                process.terminate()
                raise TimeoutError('Превышен бюджет сценария')
            time.sleep(.2)
        if process.returncode:
            raise RuntimeError(f'Сценарий завершился с кодом {process.returncode}')
        for key in ('runtime_rss_kib', 'dart_client_rss_kib'):
            if not any(row[key] > 0 for row in samples):
                raise RuntimeError(f'Нет измерений {key}')
    finally:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps({'samples': samples, 'exit_code': process.poll(),
            'scope': 'Сумма RSS процессов по типу; возможен двойной учёт разделяемых страниц. Dart CLI, не Flutter UI.'}, indent=2, ensure_ascii=False), encoding='utf-8')


if __name__ == '__main__':
    main()
