"""Проверка настоящего shell-установщика на локальных release-фикстурах."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile


@unittest.skipUnless(os.name == "posix", "Проверяется Linux-установщик")
class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.script = Path(__file__).with_name("install.sh").resolve()
        self.env = dict(os.environ, SPACE_HOME=str(self.root / "installed"), SPACE_VERSION="v99.0.0", FIXTURE=str(self.root), PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        self.command("id", '#!/bin/sh\ncase "$1" in -un) echo space-test;; *) echo 1000;; esac\n')
        self.command("uname", '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
        self.command("curl", "#!/usr/bin/env python3\nimport os,sys,shutil\nfrom pathlib import Path\na=sys.argv\nu=next(v for v in a if v.startswith('https://'))\nshutil.copyfile(Path(os.environ['FIXTURE']) / u.rsplit('/',1)[1],a[a.index('-o')+1])\n")
        self.release()

    def command(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def release(self, capability=1):
        archive = self.root / "Space-home-linux-x64.zip"
        with zipfile.ZipFile(archive, "w") as bundle:
            bundle.writestr("space-server", '#!/bin/sh\nif [ "$1" = -runtime-info ]; then echo \'{"remoteSetupVersion":' + str(capability) + '}\'; else printf "START %s\\n" "$*"; fi\n')
            bundle.writestr("postgres/bin/postgres", "fixture")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (self.root / "SHA256SUMS.txt").write_text(f"{digest}  {archive.name}\n")

    def run_installer(self, *args):
        return subprocess.run(["sh", str(self.script), *(args or ("-address", "https://example.org:9443"))], env=self.env, capture_output=True, text=True, timeout=15)

    def test_download_verify_start_and_repeat(self):
        for _ in range(2):
            result = self.run_installer()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("START start -data", result.stdout)
            self.assertIn("-address https://example.org:9443", result.stdout)
            self.assertEqual(list((self.root / "installed").glob(".download.*")), [])

    def test_bad_checksum_stops_before_execution(self):
        (self.root / "Space-home-linux-x64.zip").write_bytes(b"corrupted")
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("START", result.stdout)
        self.assertFalse((self.root / "installed/runtimes/v99.0.0").exists())

    def test_old_runtime_is_not_started(self):
        self.release(capability=0)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("START", result.stdout)

    def service_fixture(self):
        self.env['HOME'] = str(self.root / "user ' $%")
        self.command('loginctl', '#!/bin/sh\necho yes\n')
        self.command('systemctl', '''#!/usr/bin/env python3
import json, os, subprocess, sys
from pathlib import Path
a = sys.argv[1:]
root = Path(os.environ['FIXTURE'])
with (root / 'systemctl.jsonl').open('a') as log: log.write(json.dumps(a) + '\\n')
if 'show-environment' in a:
    sys.exit(int(os.environ.get('BUS_FAILURE', '0')))
if 'LoadState' in a: print('not-found')
if 'ActiveState' in a: print('active')
if 'enable' in a:
    subprocess.run([str(Path.home() / '.local/share/space-server-service/run')], check=True)
    state = Path(os.environ['SPACE_HOME']) / 'home/data/state.json'
    state.parent.mkdir(parents=True, exist_ok=True)
    state.write_text('{}')
''')

    def test_service_keeps_literal_arguments_and_private_setup(self):
        self.service_fixture()
        # Кавычки, пробелы, $, % и подстановка команды не должны исполняться.
        address = "https://example.org:9443?x='$(touch INJECTED)%"
        result = self.run_installer('--service', '-address', address)
        self.assertEqual(result.returncode, 0, result.stderr)
        unit = Path(self.env['HOME']) / '.config/systemd/user/space-server.service'
        self.assertIn('KillMode=mixed', unit.read_text())
        self.assertIn('Restart=on-failure', unit.read_text())
        self.assertIn('WantedBy=default.target', unit.read_text())
        setup = self.root / 'installed/setup.txt'
        self.assertEqual(setup.stat().st_mode & 0o777, 0o600)
        self.assertIn(address, setup.read_text())
        self.assertFalse(Path('INJECTED').exists())
        calls = [json.loads(line) for line in (self.root / 'systemctl.jsonl').read_text().splitlines()]
        self.assertIn(['--user', 'enable', '--now', 'space-server.service'], calls)

    def test_missing_user_manager_stops_before_download(self):
        self.service_fixture()
        self.env['BUS_FAILURE'] = '1'
        result = self.run_installer('--service')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('systemd', result.stderr)
        self.assertFalse((self.root / 'installed').exists())

    def test_linger_failure_does_not_report_autostart(self):
        self.service_fixture()
        self.command('loginctl', '#!/bin/sh\ncase "$1" in show-user) echo no;; *) exit 1;; esac\n')
        result = self.run_installer('--service')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sudo loginctl enable-linger', result.stderr)
        self.assertFalse((self.root / 'installed').exists())

    def test_service_rejects_managed_and_custom_data(self):
        self.service_fixture()
        for argument in ['-managed', '--managed=true', '-data=/tmp/other']:
            result = self.run_installer('--service', argument)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((self.root / 'installed').exists())


if __name__ == "__main__":
    unittest.main()
