import runpy
import unittest
from pathlib import Path

notes = runpy.run_path(str(Path(__file__).with_name("release-notes.py")))["release_notes"]


class ReleaseNotesTest(unittest.TestCase):
    def test_selects_exact_section_and_preserves_newlines(self):
        text = "# Изменения\n\n## v0.1.0 — дата\n\nПервый абзац.\n\n- Пункт\n\n## home-v0.1.0\n\nСервер.\n"
        self.assertEqual(notes(text, "v0.1.0"), "Первый абзац.\n\n- Пункт\n")
        self.assertEqual(notes(text, "home-v0.1.0"), "Сервер.\n")

    def test_missing_empty_duplicate_and_invalid_tags_fail(self):
        for text, tag in [("## v0.1.1\nДругой", "v0.1.0"), ("## v0.1.0\n", "v0.1.0"),
                          ("## v0.1.0\nПервый\n## v0.1.0\nВторой", "v0.1.0"), ("", "v../bad")]:
            with self.assertRaises(ValueError):
                notes(text, tag)


if __name__ == "__main__":
    unittest.main()
