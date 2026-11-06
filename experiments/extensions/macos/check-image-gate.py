"""Проверка отказа до входа в блок запуска launcher на настоящем образе CI."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from verified_image import verified_image


def reject(name, mutate, key=None):
    with tempfile.TemporaryDirectory() as temporary:
        source = Path(temporary) / "image"
        shutil.copytree("vm-image", source)
        mutate(source)
        try:
            with verified_image(source, key):
                raise AssertionError("Недоверенный образ достиг блока запуска")
        except subprocess.CalledProcessError as error:
            assert error.returncode == 1
        return {"name": name, "rejected_before_launch": True}


def corrupt(source, name):
    with (source / name).open("r+b") as file:
        value = file.read(1)
        file.seek(0)
        file.write(bytes([value[0] ^ 1]))


cases = [reject("tampered-" + name, lambda source, name=name: corrupt(source, name))
         for name in ("vmlinuz", "initramfs.gz", "image.json", "image.sig")]
cases.append(reject("unknown-key", lambda source: None, "00" * 32))
cases.append(reject("missing-signature", lambda source: (source / "image.sig").unlink()))
with verified_image() as (snapshot, images):
    assert all(Path(path).is_file() for path in images)
    result = {"manifest_sha256": snapshot["manifest_sha256"], "arch": snapshot["arch"],
              "public_key": os.environ["P1_IMAGE_PUBLIC_KEY"],
              "key_source": "independent CI job output; ephemeral fixture, not production publisher",
              "valid_image_accepted": True, "cases": cases}
assert not Path(snapshot["directory"]).exists(), "Снимок не удалён"
Path("vm-image-trust.json").write_text(json.dumps(result, indent=2) + "\n")
