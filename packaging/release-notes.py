"""Проверка тега и выбор описания выпуска из Changelog.md."""
import re
import sys
from pathlib import Path

TAG = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?")

def tag_info(tag):
    match = TAG.fullmatch(tag)
    if match is None:
        raise ValueError("Нужен тег vX.Y.Z или vX.Y.Z-rc.N")
    return {"version": tag[1:], "native_version": ".".join(match.group(i) for i in (1, 2, 3)), "prerelease": match.group(4) is not None}

def release_notes(changelog, tag, allow_unreleased=False):
    unreleased = allow_unreleased and tag == "Не выпущено"
    if not unreleased:
        tag_info(tag)
    sections = list(re.finditer(r"^## (.+)$", changelog, re.MULTILINE))
    matches = []
    for index, section in enumerate(sections):
        heading = section.group(1).strip()
        version = heading if unreleased else heading.split()[0].strip("[]")
        if version == tag:
            end = sections[index + 1].start() if index + 1 < len(sections) else len(changelog)
            matches.append(changelog[section.end():end].strip())
    if len(matches) != 1 or not matches[0]:
        raise ValueError("Нужен ровно один непустой раздел Changelog.md для " + tag)
    return matches[0] + "\n"

if __name__ == "__main__":
    tag, source, destination = sys.argv[1:]
    Path(destination).write_text(release_notes(Path(source).read_text(encoding="utf-8"), tag), encoding="utf-8")
