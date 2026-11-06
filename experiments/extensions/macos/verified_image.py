"""Ключ приходит из доверенной конфигурации CI, не из скачанного образа."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import tempfile


@contextmanager
def verified_image(source="vm-image", key=None):
    with tempfile.TemporaryDirectory(prefix='space-vm-verified-') as parent:
        result = subprocess.run(['./image-verify', str(source), key if key is not None else os.environ['P1_IMAGE_PUBLIC_KEY'], parent],
                                check=True, capture_output=True, text=True, timeout=30)
        snapshot = json.loads(result.stdout)
        directory = Path(snapshot['directory'])
        assert directory.parent == Path(parent)
        yield snapshot, [str(directory / 'vmlinuz'), str(directory / 'initramfs.gz')]
