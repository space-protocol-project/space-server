"""Проверка настоящего shell-установщика на локальных release-фикстурах."""
import hashlib
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
        self.command("id", "#!/bin/sh\necho 1000\n")
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

    def run_installer(self):
        return subprocess.run(["sh", str(self.script), "-address", "https://example.org:9443"], env=self.env, capture_output=True, text=True)

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


if __name__ == "__main__":
    unittest.main()
