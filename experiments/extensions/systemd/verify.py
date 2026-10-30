"""Аварийная проверка реального systemd только на одноразовом GitHub runner."""
import json
import os
from pathlib import Path
import signal
import subprocess
import time

UNIT = 'space-p1-supervisor.service'
DROP = Path('/run/systemd/system') / (UNIT + '.d') / 'test.conf'
CONTROL = Path('/run/space-p1-ci-control')
RESULTS = []


def ctl(*args, check=True):
    return subprocess.run(['systemctl', *args], check=check, capture_output=True,
                          text=True, timeout=20).stdout.strip()


def state():
    text = ctl('show', UNIT, '-p', 'MainPID', '-p', 'ActiveState', '-p', 'SubState',
               '-p', 'NRestarts', '-p', 'Result', '-p', 'ControlGroup')
    return dict(line.split('=', 1) for line in text.splitlines())


def wait(predicate, description, seconds=20):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        result = predicate()
        if result:
            return result
        time.sleep(.02)
    raise AssertionError(f'{description}: {state()}')


def status():
    path = CONTROL / 'status.json'
    return json.loads(path.read_text()) if path.exists() else {}


def process_token(pid):
    try:
        fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
        return None if fields[0] == 'Z' else fields[19]
    except FileNotFoundError:
        return None


def processes(group):
    return {int(pid) for file in group.rglob('cgroup.procs')
            for pid in file.read_text().split()}


def reset():
    ctl('stop', UNIT, check=False)
    ctl('reset-failed', UNIT, check=False)
    for name in ['once', 'status.json', 'status.json.tmp']:
        (CONTROL / name).unlink(missing_ok=True)


def override(text):
    DROP.parent.mkdir(exist_ok=True)
    DROP.write_text('[Service]\nExecStart=\n' + text)
    ctl('daemon-reload')


def assert_stopped(restarts):
    stopped = state()
    terminal = stopped['ActiveState'] == 'inactive' or (
        stopped['ActiveState'] == 'failed' and stopped['Result'] == 'signal')
    assert terminal and stopped['MainPID'] == '0', stopped
    assert int(stopped['NRestarts']) == restarts, stopped
    return stopped


def smoke():
    ctl('start', UNIT)
    current = state()
    assert current['ActiveState'] == 'active' and int(current['MainPID']) > 0
    root = Path('/sys/fs/cgroup') / current['ControlGroup'].lstrip('/')
    assert (root / 'workers').is_dir()
    pid = int(current['MainPID'])
    ctl('stop', UNIT)
    time.sleep(1.2)
    stopped = assert_stopped(0)
    assert process_token(pid) is None
    RESULTS.append({'scenario': 'launcher-ready-and-stop', 'result': 'pass',
                    'stop_state': stopped['ActiveState'], 'stop_result': stopped['Result']})


def crash(stage):
    reset()
    override('ExecStart=/opt/space-p1/isolation.test -test.run=^TestServiceHelper$ -test.timeout=90s\n'
             'WorkingDirectory=/opt/space-p1\n'
             f'Environment=P1_SERVICE_CONTROL={CONTROL} P1_SERVICE_STAGE={stage} '
             'P1_WORKER=/opt/space-p1/p1-worker P1_PRESSURE=/opt/space-p1/p1-pressure\n')
    ctl('start', '--no-block', UNIT)
    old = wait(lambda: status() if status().get('phase') == stage else None, 'барьер аварии')
    current = state()
    expected = 'active' if stage == 'running' else 'activating'
    assert current['ActiveState'] == expected, current
    root = Path('/sys/fs/cgroup') / current['ControlGroup'].lstrip('/')
    pids = processes(root)
    assert old['pid'] in pids
    if stage == 'running':
        assert len(pids) >= 3, pids
    tokens = {pid: process_token(pid) for pid in pids}
    assert all(token is not None for token in tokens.values()), tokens
    started = time.monotonic()
    os.kill(old['pid'], signal.SIGKILL)
    wait(lambda: all(process_token(pid) != token for pid, token in tokens.items()),
         'завершение старого дерева', seconds=5)
    cleanup_ms = (time.monotonic() - started) * 1000
    # RestartSec даёт отдельное окно: очистка должна завершиться до нового MainPID.
    between = state()
    assert between['MainPID'] == '0' and between['SubState'] == 'auto-restart', between
    fresh = wait(lambda: status() if status().get('healthy') and status().get('pid') != old['pid']
                 and state()['ActiveState'] == 'active' else None, 'готовность нового supervisor')
    after = state()
    assert int(after['NRestarts']) == 1, after
    assert not any(process_token(pid) == token for pid, token in tokens.items())
    # Готовность опубликована после реального echo и отсутствия старых call-групп.
    assert fresh['group'] and not list(Path(fresh['group']).glob('call-*'))
    RESULTS.append({'scenario': stage, 'result': 'pass', 'old_processes': len(pids),
                    'cleanup_ms': round(cleanup_ms, 3),
                    'ready_ms': round((time.monotonic() - started) * 1000, 3),
                    'restarts': int(after['NRestarts']), 'healthy_wasm': True})
    ctl('stop', UNIT)
    time.sleep(1.2)
    assert_stopped(1)


def restart_limit():
    reset()
    override('ExecStart=/opt/space-p1/p1-supervisor -worker /missing-p1-worker\n')
    ctl('start', '--no-block', UNIT)
    failed = wait(restart_exhausted, 'ограничение повторных отказов')
    time.sleep(2.2)
    assert restart_exhausted(), state()
    RESULTS.append({'scenario': 'invalid-worker-restart-limit', 'result': 'pass',
                    'restarts': int(failed['NRestarts']), 'ready': False,
                    'terminal_result': failed['Result']})


def restart_exhausted():
    current = state()
    if (current['ActiveState'] == 'failed' and current['SubState'] == 'failed'
            and current['MainPID'] == '0' and current['NRestarts'] == '5'):
        return current
    return None


try:
    CONTROL.mkdir(mode=0o700, exist_ok=True)
    subprocess.run(['systemd-analyze', 'verify', '/run/systemd/system/' + UNIT], check=True)
    ctl('daemon-reload')
    smoke()
    for stage in ['before-host', 'before-ready', 'running']:
        crash(stage)
    restart_limit()
finally:
    Path('service-results.json').write_text(json.dumps(RESULTS, indent=2) + '\n')
    log = subprocess.run(['journalctl', '-u', UNIT, '--no-pager'], capture_output=True, text=True)
    Path('service-journal.txt').write_text(log.stdout)
    ctl('stop', UNIT, check=False)
    DROP.unlink(missing_ok=True)
    Path('/run/systemd/system/' + UNIT).unlink(missing_ok=True)
    ctl('daemon-reload')
    ctl('reset-failed', UNIT, check=False)
