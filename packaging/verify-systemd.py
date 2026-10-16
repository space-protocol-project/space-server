"""Настоящие user-systemd и PostgreSQL в одноразовом пользователе CI Linux."""
import importlib.util
import os
from pathlib import Path
import pwd
import signal
import subprocess
import time
import urllib.parse


USER = 'space-systemd-ci'
account = pwd.getpwnam(USER)
home = Path(account.pw_dir)
base = home / '.local/share/space-server'
env = dict(os.environ, HOME=str(home), XDG_RUNTIME_DIR=f'/run/user/{account.pw_uid}',
           DBUS_SESSION_BUS_ADDRESS=f'unix:path=/run/user/{account.pw_uid}/bus',
           SPACE_VERSION='v0.3.3')


def user(*args, check=True):
    result = subprocess.run(['sudo', '-u', USER, 'env', *[f'{k}={env[k]}' for k in
        ['HOME', 'XDG_RUNTIME_DIR', 'DBUS_SESSION_BUS_ADDRESS', 'SPACE_VERSION']], *args],
        capture_output=True, text=True, timeout=150)
    if check and result.returncode:
        # stdout установщика может содержать одноразовую ссылку: не печатаем его.
        raise RuntimeError(f'Команда {args[0]} завершилась с кодом {result.returncode}: {result.stderr[-3000:]}')
    return result


def ctl(*args, check=True):
    return user('systemctl', '--user', *args, check=check).stdout.strip()


spec = importlib.util.spec_from_file_location('published', Path(__file__).with_name('verify-published-runtime.py'))
published = importlib.util.module_from_spec(spec)
spec.loader.exec_module(published)


def ready(previous=''):
    deadline = time.monotonic() + 100
    while time.monotonic() < deadline:
        setup = base / 'setup.txt'
        lines = setup.read_text().splitlines() if setup.exists() else []
        links = [line for line in lines if line.startswith('https://') and 'setup=' in line]
        if links and links[0] != previous and ctl('is-active', 'space-server', check=False) == 'active':
            link = links[0]
            port = urllib.parse.urlsplit(link).port
            try:
                published.verify_tls(link, port)
                return link
            except (OSError, ValueError):
                pass
        time.sleep(1)
    raise TimeoutError('Служба не восстановила HTTPS за 100 секунд')


def sql(statement):
    data = base / 'home/data'
    port = (data / 'postgres/postmaster.pid').read_text().splitlines()[3]
    runtime = base / 'runtimes/v0.3.3'
    environment = dict(os.environ, PGPASSWORD=(data / 'database-password').read_text(),
                       LD_LIBRARY_PATH=str(runtime / 'postgres/lib'))
    result = subprocess.run([str(runtime / 'postgres/bin/psql'), '-h', '127.0.0.1', '-p', port,
        '-U', 'space', '-d', 'postgres', '-At', '-v', 'ON_ERROR_STOP=1', '-c', statement],
        env=environment, capture_output=True, text=True, check=True, timeout=10)
    return result.stdout.strip()


def identity(link):
    fields = urllib.parse.parse_qs(urllib.parse.urlsplit(link).fragment)
    return fields['server'], fields['tls']


def verify():
    # Эти параметры относятся только к отдельному тестовому пользователю CI.
    user('sh', '/tmp/space-systemd-install.sh', '--service', '-address', 'https://127.0.0.1:18443', '-listen', '127.0.0.1:18443')
    first = ready()
    print('Первый запуск и приватная ссылка: OK', flush=True)
    assert ctl('is-enabled', 'space-server') == 'enabled'
    assert (base / 'setup.txt').stat().st_mode & 0o777 == 0o600
    unit = home / '.config/systemd/user/space-server.service'
    user('systemd-analyze', '--user', 'verify', str(unit))
    sql("CREATE TABLE systemd_smoke(value text); INSERT INTO systemd_smoke VALUES ('сохранено');")

    # Повторная установка не меняет идентичность и не удаляет базу.
    user('sh', '/tmp/space-systemd-install.sh', '--service')
    second = ready(first)
    assert identity(first) == identity(second)
    assert sql('SELECT value FROM systemd_smoke') == 'сохранено'
    print('Повторная установка и сохранение БД: OK', flush=True)

    old_pid = int(ctl('show', 'space-server', '-p', 'MainPID', '--value'))
    os.kill(old_pid, signal.SIGKILL)
    recovered = ready(second)
    assert identity(first) == identity(recovered)
    assert sql('SELECT value FROM systemd_smoke') == 'сохранено'
    print('Восстановление после SIGKILL: OK', flush=True)

    # Перезапуск user manager запускает enabled-службу без SSH-сессии.
    subprocess.run(['systemctl', 'restart', f'user@{account.pw_uid}.service'], check=True, timeout=100)
    rebooted = ready(recovered)
    assert identity(first) == identity(rebooted)
    assert sql('SELECT value FROM systemd_smoke') == 'сохранено'
    print('Запуск user manager без SSH, прежние TLS и БД: OK', flush=True)
    journal = user('journalctl', '--user', '-u', 'space-server', '--no-pager').stdout
    assert 'setup_' not in journal and '#setup=' not in journal

    pg_pid = int((base / 'home/data/postgres/postmaster.pid').read_text().splitlines()[0])
    ctl('stop', 'space-server')
    assert not (base / 'home/data/state.json').exists()
    try:
        os.kill(pg_pid, 0)
    except ProcessLookupError:
        pass
    else:
        raise AssertionError('PostgreSQL осталась работать после stop')
    print('Штатная остановка PostgreSQL: OK', flush=True)
    ctl('disable', 'space-server')
    assert ctl('is-enabled', 'space-server', check=False) == 'disabled'
    # Проверяем загрузочную цель без повторного пересоздания cgroup менеджера:
    # в образе Actions его второй быстрый restart нестабилен (219/CGROUP).
    ctl('stop', 'default.target')
    ctl('start', 'default.target')
    assert ctl('is-active', 'space-server', check=False) != 'active'
    assert (base / 'home/data/postgres/PG_VERSION').exists()
    print('systemd: выход установщика, повторная установка, crash-restart, запуск без SSH, TLS/БД, приватная ссылка и stop/disable проверены')


if __name__ == '__main__':
    verify()
