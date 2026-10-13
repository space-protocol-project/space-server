"""Проверить выпуск до сборки и передать метаданные GitHub Actions."""
import os
from pathlib import Path
import runpy
import sys

helpers = runpy.run_path(str(Path(__file__).with_name("release-notes.py")))
tag = sys.argv[1]
meta = helpers["tag_info"](tag)
notes = helpers["release_notes"](Path("Changelog.md").read_text(encoding="utf-8"), tag)
if len(notes.encode("utf-8")) > 8192:
    raise ValueError("Описание выпуска должно помещаться в 8 КиБ")
output = os.environ.get("GITHUB_OUTPUT")
if output:
    with Path(output).open("a", encoding="utf-8") as stream:
        for key, value in meta.items():
            stream.write(f"{key}={str(value).lower() if isinstance(value, bool) else value}\n")
print("Проверен выпуск " + tag)
