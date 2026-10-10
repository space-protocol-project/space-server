"""Взять описание выпуска из одноимённого раздела Changelog.md."""
import re
import sys
from pathlib import Path


def release_notes(changelog, tag):
    if not re.fullmatch(r"(?:home-)?v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", tag):
        raise ValueError("Некорректный тег выпуска")
    sections = list(re.finditer(r"^## (.+)$", changelog, re.MULTILINE))
    matches = []
    for index, section in enumerate(sections):
        title = section.group(1).strip()
        version = title.split()[0].strip("[]")
        if version != tag:
            continue
        end = sections[index + 1].start() if index + 1 < len(sections) else len(changelog)
        content = changelog[section.end():end].strip()
        if content:
            matches.append(content)
    if len(matches) != 1:
        raise ValueError("Нужен ровно один непустой раздел Changelog.md для " + tag)
    return matches[0] + "\n"


if __name__ == "__main__":
    tag, source, destination = sys.argv[1:]
    Path(destination).write_text(release_notes(Path(source).read_text(encoding="utf-8"), tag), encoding="utf-8")
