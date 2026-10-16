"""Запустить опубликованный Linux-установщик в одноразовом каталоге CI."""
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request


def setup_link(process):
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    deadline = time.monotonic() + 180
    pending = b""
    try:
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"Установщик завершился до настройки: {process.returncode}")
            for key, _ in selector.select(timeout=1):
                pending += os.read(key.fd, 8192)
                lines = pending.split(b"\n")
                pending = lines.pop()
                for line in lines:
                    if line.startswith(b"https://") and b"#" in line:
                        return line.decode().strip()
        raise TimeoutError("Установщик не выдал ссылку настройки за 180 секунд")
    finally:
        selector.close()


def verify_tls(link, port):
    parsed = urllib.parse.urlsplit(link)
    fields = urllib.parse.parse_qs(parsed.fragment, strict_parsing=True)
    pin, server_id = fields["tls"][0], fields["server"][0]
    if not re.fullmatch(r"setup_[A-Za-z0-9_-]{43}", fields["setup"][0]):
        raise ValueError("Некорректный одноразовый код")
    # Как в приложении: доверие держится на точном pin, не на CA или имени хоста.
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    with socket.create_connection(("127.0.0.1", port), timeout=5) as raw:
        with context.wrap_socket(raw, server_hostname="127.0.0.1") as secured:
            if hashlib.sha256(secured.getpeercert(binary_form=True)).hexdigest() != pin:
                raise ValueError("TLS pin опубликованного runtime не совпал")
            secured.sendall(f"GET /.well-known/space-protocol HTTP/1.0\r\nHost: 127.0.0.1:{port}\r\n\r\n".encode())
            response = secured.makefile("rb").read(32769)
    if len(response) > 32768 or response.split(b"\r\n", 1)[0].split()[1] != b"200":
        raise ValueError("Не получен ограниченный discovery по HTTPS")
    document = json.loads(response.split(b"\r\n\r\n", 1)[1])
    if document["server_id"] != server_id:
        raise ValueError("Server ID discovery не совпал со ссылкой")


def verify(version):
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version):
        raise ValueError("Нужен тег vX.Y.Z")
    with tempfile.TemporaryDirectory(prefix="space-published-runtime-") as directory:
        root = Path(directory)
        url = f"https://raw.githubusercontent.com/space-protocol-project/space-server/{version}/packaging/install.sh"
        with urllib.request.urlopen(url, timeout=30) as response:
            script = response.read(65537)
        if len(script) > 65536:
            raise ValueError("Слишком большой установщик")
        installer = root / "install.sh"
        installer.write_bytes(script)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        env = dict(os.environ, SPACE_HOME=str(root / "home"), SPACE_VERSION=version)
        with (root / "stderr.log").open("w") as errors:
            process = subprocess.Popen(["sh", str(installer), "-address", f"https://127.0.0.1:{port}", "-listen", f"127.0.0.1:{port}"], env=env, stdout=subprocess.PIPE, stderr=errors, bufsize=0, start_new_session=True)
            ready = False
            try:
                link = setup_link(process)
                ready = True
                verify_tls(link, port)
            finally:
                # Установщик exec-ом заменяется сервером; SIGTERM закрывает его БД.
                if process.poll() is None:
                    if ready:
                        process.terminate()
                    else:
                        os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=30)
            if process.returncode != 0:
                raise RuntimeError(f"Нештатная остановка runtime: {process.returncode}")
        print(f"{version}: настоящий Linux-установщик, SHA-256, PostgreSQL, TLS pin и setup-ссылка проверены")


if __name__ == "__main__":
    verify(sys.argv[1])
