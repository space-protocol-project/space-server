"""Собрать проверяемый архив сервера с собственной PostgreSQL только в CI."""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import urllib.request
import zipfile

VERSION = os.environ.get("GITHUB_REF_NAME", "v0.1.0")
if not VERSION.startswith("v"):
    VERSION = "v0.1.0"
PG_VERSION = "17.11"
ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "dist"
STAGE = DIST / "home"


def fetch(url, destination):
    urllib.request.urlretrieve(url, destination)


def run(*args, cwd=None, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def build():
    DIST.mkdir(exist_ok=True)
    STAGE.mkdir(exist_ok=True)
    system = platform.system()
    architecture = "arm64" if platform.machine().lower() in ("arm64", "aarch64") else "x64"
    target = {"Windows": "windows", "Darwin": "macos", "Linux": "linux"}[system] + "-" + architecture
    with tempfile.TemporaryDirectory() as work:
        work = Path(work)
        if system == "Windows":
            source = work / "postgres.zip"
            fetch(f"https://get.enterprisedb.com/postgresql/postgresql-{PG_VERSION}-1-windows-x64-binaries.zip", source)
            with zipfile.ZipFile(source) as archive:
                for entry in archive.infolist():
                    name = entry.filename
                    if name.startswith(("pgsql/bin/", "pgsql/lib/", "pgsql/share/", "pgsql/doc/")):
                        archive.extract(entry, work)
            shutil.copytree(work / "pgsql", STAGE / "postgres")
            fetch("https://raw.githubusercontent.com/postgres/postgres/REL_17_11/COPYRIGHT", STAGE / "POSTGRESQL-COPYRIGHT")
        else:
            source = work / "postgres.tar.bz2"
            url = f"https://ftp.postgresql.org/pub/source/v{PG_VERSION}/postgresql-{PG_VERSION}.tar.bz2"
            fetch(url, source)
            expected = urllib.request.urlopen(url + ".sha256").read().decode().split()[0]
            if hashlib.sha256(source.read_bytes()).hexdigest() != expected:
                raise RuntimeError("Контрольная сумма исходников PostgreSQL не совпадает")
            import tarfile
            with tarfile.open(source) as archive:
                archive.extractall(work, filter="data")
            src = work / f"postgresql-{PG_VERSION}"
            run("./configure", "--prefix=" + str(STAGE / "postgres"), "--without-readline", "--without-zlib", "--without-icu", "--without-lz4", "--without-zstd", "--disable-rpath", cwd=src)
            run("make", "-j3", cwd=src)
            run("make", "install", cwd=src)
            shutil.copy(src / "COPYRIGHT", STAGE / "POSTGRESQL-COPYRIGHT")
            if system == "Darwin":
                # Убираем пути одноразового runner из ссылок на libpq.
                for path in list((STAGE / "postgres/bin").iterdir()) + list((STAGE / "postgres/lib").glob("*.dylib")):
                    if not path.is_file() or path.is_symlink():
                        continue
                    output = subprocess.check_output(["otool", "-L", str(path)], text=True)
                    for line in output.splitlines()[1:]:
                        dependency = line.strip().split(" ")[0]
                        if dependency.startswith(str(STAGE / "postgres/lib")):
                            run("install_name_tool", "-change", dependency, "@loader_path/../lib/" + Path(dependency).name, str(path))
                    run("codesign", "--force", "--sign", "-", str(path))
        executable = "space-server.exe" if system == "Windows" else "space-server"
        flags = ["-ldflags=-H=windowsgui"] if system == "Windows" else []
        run("go", "build", "-trimpath", *flags, "-o", str(STAGE / executable), "./cmd/space-server", cwd=ROOT)
        info = json.loads(subprocess.check_output([str(STAGE / executable), "-runtime-info"], text=True))
        if info["format"] != 1 or info["postgresMajor"] != int(PG_VERSION.split(".")[0]):
            raise RuntimeError("Сервер и комплект PostgreSQL несовместимы")
        (STAGE / "runtime-info.json").write_text(json.dumps(info, indent=2) + "\n", encoding="utf-8")
        (STAGE / "SOURCE.txt").write_text("Space Protocol: https://github.com/space-protocol-project/space-server\nPostgreSQL 17.11: https://www.postgresql.org/\n", encoding="utf-8")
        # Проверка запуска и сохранения базы выполняется до публикации архива.
        env = dict(os.environ, SPACE_HOME_RUNTIME=str(STAGE))
        run("go", "test", "./internal/home", "-count=1", "-timeout=5m", cwd=ROOT, env=env)
        filename = f"Space-home-{target}.zip"
        with zipfile.ZipFile(DIST / filename, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for path in STAGE.rglob("*"):
                if path.is_file():
                    archive.write(path, path.relative_to(STAGE).as_posix())
        digest = hashlib.sha256((DIST / filename).read_bytes()).hexdigest()
        manifest = {"version": VERSION, "platform": target, "url": f"https://github.com/space-protocol-project/space-server/releases/download/{VERSION}/{filename}", "sha256": digest, "bytes": (DIST / filename).stat().st_size}
        (DIST / f"{target}.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    build()
