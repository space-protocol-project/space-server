import io
import runpy
from pathlib import Path
import unittest
from unittest.mock import patch


verify_go_version = runpy.run_path(
    str(Path(__file__).with_name("build-home-runtime.py"))
)["verify_go_version"]


class RuntimeGoTest(unittest.TestCase):
    def test_windows_console_encoding(self):
        with patch("sys.stdout", io.TextIOWrapper(io.BytesIO(), encoding="cp1252")):
            with patch("subprocess.check_output", return_value="server.exe: go1.27.3\n"):
                self.assertEqual(verify_go_version(Path("server.exe"), "1.27.3"), "go1.27.3")

    def test_reads_version_from_binary_with_spaces_in_path(self):
        binary = Path("runtime folder/space-server")
        with patch("subprocess.check_output", return_value=f"{binary}: go1.27.3\n") as read:
            self.assertEqual(verify_go_version(binary, "1.27.3"), "go1.27.3")
            read.assert_called_once_with(["go", "version", str(binary)], text=True)

    def test_rejects_old_and_unexpected_toolchains(self):
        for version in ["go1.27.2", "go1.27.4", "devel"]:
            with self.subTest(version=version):
                with patch("subprocess.check_output", return_value=f"space-server: {version}\n"):
                    with self.assertRaises(RuntimeError):
                        verify_go_version(Path("space-server"), "1.27.3")
