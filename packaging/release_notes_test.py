import runpy
import unittest
from pathlib import Path

notes = runpy.run_path(str(Path(__file__).with_name("release-notes.py")))["release_notes"]


class ReleaseNotesTest(unittest.TestCase):
    def test_unreleased_requires_explicit_permission(self):
        text = "## Не выпущено\nКандидат.\n\n## v0.1.0\nСтарый выпуск.\n"
        with self.assertRaises(ValueError):
            notes(text, "Не выпущено")
        self.assertEqual(notes(text, "Не выпущено", allow_unreleased=True), "Кандидат.\n")

    def test_unreleased_is_unique_and_nonempty(self):
        for text in ["## Не выпущено\n\n## v0.1.0\nВыпуск", "## Не выпущено\nОдин\n## Не выпущено\nДва"]:
            with self.assertRaises(ValueError):
                notes(text, "Не выпущено", allow_unreleased=True)
    def test_selects_exact_section_and_preserves_newlines(self):
        text = "# Изменения\n\n## v0.1.0 — дата\n\nПервый абзац.\n\n- Пункт\n\n## v0.2.0\n\nСервер.\n"
        self.assertEqual(notes(text, "v0.1.0"), "Первый абзац.\n\n- Пункт\n")
        self.assertEqual(notes(text, "v0.2.0"), "Сервер.\n")

    def test_missing_empty_duplicate_and_invalid_tags_fail(self):
        for text, tag in [("## v0.1.1\nДругой", "v0.1.0"), ("## v0.1.0\n", "v0.1.0"),
                          ("## v0.1.0\nПервый\n## v0.1.0\nВторой", "v0.1.0"), ("", "v../bad")]:
            with self.assertRaises(ValueError):
                notes(text, tag)

    def test_empty_duplicate_and_invalid_semver_fail(self):
        with self.assertRaises(ValueError):
            notes("## v0.3.0\n\n## v0.3.0\nЗаполненный раздел", "v0.3.0")
        for tag in ["v01.2.3", "v1.2.3-..", "v1.2.3-rc.01", "v1.2", "v1.2.3+build"]:
            with self.assertRaises(ValueError):
                notes("## " + tag + "\nПункт", tag)
        self.assertEqual(notes("## v0.3.0-rc.1\nПредварительный выпуск", "v0.3.0-rc.1"), "Предварительный выпуск\n")


if __name__ == "__main__":
    unittest.main()
