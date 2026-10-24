import runpy
import unittest
from pathlib import Path


build_version = runpy.run_path(str(Path(__file__).with_name("build-home-runtime.py")))["build_version"]


class RuntimeCandidateTest(unittest.TestCase):
    def test_candidate_has_exact_version_without_release_tag(self):
        self.assertEqual(build_version({"GITHUB_REF": "refs/heads/candidate", "SPACE_RUNTIME_CANDIDATE_VERSION": "v0.7.15"}), "v0.7.15")

    def test_tag_cannot_be_overridden(self):
        with self.assertRaises(ValueError):
            build_version({"GITHUB_REF": "refs/tags/v0.7.15", "GITHUB_REF_NAME": "v0.7.15", "SPACE_RUNTIME_CANDIDATE_VERSION": "v9.0.0"})

    def test_candidate_is_validated_and_regular_branch_remains_dev(self):
        with self.assertRaises(ValueError):
            build_version({"SPACE_RUNTIME_CANDIDATE_VERSION": "../../candidate"})
        self.assertEqual(build_version({"GITHUB_REF_NAME": "voice-rooms"}), "v0.1.0")


if __name__ == "__main__":
    unittest.main()
