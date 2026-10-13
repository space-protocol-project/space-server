"""Публиковать проверенные артефакты; незавершённый выпуск остаётся черновиком."""
import hashlib
import json
from pathlib import Path
import runpy
import subprocess
import sys

ASSETS = {
    "app": ["Space-Setup-windows-x64.exe", "Space-macos-arm64.dmg", "Space-linux-x64.run"],
    "server": ["Space-home-windows-x64.zip", "Space-home-macos-arm64.zip", "Space-home-linux-x64.zip",
               "windows-x64.json", "macos-arm64.json", "linux-x64.json"],
}


def publish(project, tag, directory, runner=subprocess.run):
    helpers = runpy.run_path(str(Path(__file__).with_name("release-notes.py")))
    meta = helpers["tag_info"](tag)
    directory = Path(directory)
    files = [directory / name for name in ASSETS[project]]
    if not all(path.is_file() and path.stat().st_size > 0 for path in files):
        raise ValueError("Нет полного комплекта артефактов выпуска")
    notes = directory / "release-notes.md"
    if not notes.is_file() or not notes.read_text(encoding="utf-8").strip():
        raise ValueError("Нет описания выпуска")
    checksums = directory / "SHA256SUMS.txt"
    with checksums.open("w", encoding="utf-8", newline="\n") as output:
        for path in files:
            with path.open("rb") as source:
                digest = hashlib.file_digest(source, "sha256").hexdigest()
            output.write(f"{digest}  {path.name}\n")
    files.append(checksums)
    current = runner(["gh", "release", "view", tag, "--json", "isDraft,assets"],
                     capture_output=True, text=True, check=False)
    if current.returncode == 0:
        release = json.loads(current.stdout)
        if not release["isDraft"]:
            names = {asset["name"] for asset in release["assets"]}
            if not {path.name for path in files}.issubset(names):
                raise ValueError("Опубликованный выпуск неполон; требуется ручная проверка")
            print("Выпуск уже опубликован: " + tag)
            return
        runner(["gh", "release", "upload", tag, *map(str, files), "--clobber"], check=True)
    else:
        title = "Space" if project == "app" else "Домашний сервер Space"
        runner(["gh", "release", "create", tag, *map(str, files), "--verify-tag", "--draft",
                "--title", f"{title} {tag}", "--notes-file", str(notes)], check=True)
    flags = ["--draft=false", f"--prerelease={str(meta['prerelease']).lower()}"]
    if meta["prerelease"]:
        flags.append("--latest=false")
    runner(["gh", "release", "edit", tag, "--notes-file", str(notes), *flags], check=True)


if __name__ == "__main__":
    publish(*sys.argv[1:])
