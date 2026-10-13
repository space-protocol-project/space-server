import json
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest

helpers = runpy.run_path(str(Path(__file__).with_name("publish-release.py")))


class PublishReleaseTest(unittest.TestCase):
    def prepare(self, root, project):
        for name in helpers["ASSETS"][project]:
            (root / name).write_bytes(b"checked artifact")
        (root / "release-notes.md").write_text("Изменения выпуска", encoding="utf-8")

    def run_publish(self, project, tag, root, release=None, fail_upload=False):
        calls = []
        def runner(args, **options):
            calls.append(args)
            if args[2] == "view":
                return subprocess.CompletedProcess(args, 1 if release is None else 0,
                                                   "" if release is None else json.dumps(release))
            if fail_upload and args[2] == "upload":
                raise subprocess.CalledProcessError(1, args)
            return subprocess.CompletedProcess(args, 0)
        helpers["publish"](project, tag, root, runner=runner)
        return calls

    def test_complete_artifacts_are_published_after_draft_creation(self):
        for project in helpers["ASSETS"]:
            with tempfile.TemporaryDirectory() as work:
                root = Path(work)
                self.prepare(root, project)
                calls = self.run_publish(project, "v0.3.0", root)
                self.assertEqual([call[2] for call in calls], ["view", "create", "edit"])
                self.assertIn("--draft", calls[1])
                self.assertIn("--verify-tag", calls[1])
                self.assertIn("--prerelease=false", calls[-1])
                self.assertIn("--draft=false", calls[-1])
                self.assertEqual(len((root / "SHA256SUMS.txt").read_text().splitlines()), len(helpers["ASSETS"][project]))

    def test_preview_and_draft_retry(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            self.prepare(root, "app")
            calls = self.run_publish("app", "v0.3.0-rc.1", root, {"isDraft": True})
            self.assertEqual([call[2] for call in calls], ["view", "upload", "edit"])
            self.assertIn("--clobber", calls[1])
            self.assertIn("--prerelease=true", calls[-1])
            self.assertIn("--latest=false", calls[-1])
            with self.assertRaises(subprocess.CalledProcessError):
                self.run_publish("app", "v0.3.0-rc.1", root, {"isDraft": True}, fail_upload=True)

    def test_published_release_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            self.prepare(root, "server")
            release = {"isDraft": False, "assets": [{"name": name} for name in helpers["ASSETS"]["server"] + ["SHA256SUMS.txt"]]}
            calls = self.run_publish("server", "v0.3.0", root, release)
            self.assertEqual(len(calls), 1)
            release["assets"] = []
            with self.assertRaises(ValueError):
                self.run_publish("server", "v0.3.0", root, release)

    def test_incomplete_artifacts_fail_before_github_request(self):
        with tempfile.TemporaryDirectory() as work:
            with self.assertRaises(ValueError):
                self.run_publish("server", "v0.3.0", Path(work))


if __name__ == "__main__":
    unittest.main()
