"""Ограниченный запуск VM и снимки RSS launcher; не измерение общего RSS VMM."""
import json
import subprocess
import time
from pathlib import Path

samples = []
started = time.monotonic()
with open("vm-launcher.log", "w") as log:
    process = subprocess.Popen(["./guest-launcher", "vm-image/vmlinuz", "vm-image/initramfs.gz", "guest-console.txt"], stdout=log, stderr=log)
    try:
        while process.poll() is None:
            if time.monotonic() - started > 260:
                raise TimeoutError("VM did not stop")
            rss = subprocess.run(["ps", "-o", "rss=", "-p", str(process.pid)], capture_output=True, text=True).stdout.strip()
            if rss:
                samples.append({"seconds": round(time.monotonic()-started, 3), "launcher_rss_kib": int(rss)})
            time.sleep(1)
    finally:
        if process.poll() is None:
            process.kill()
        process.wait()
        Path("vm-memory.json").write_text(json.dumps({"guest_ram_bytes": 1073741824, "launcher_samples": samples, "exit_code": process.returncode, "aggregate_vmm_budget_verified": False}, indent=2)+"\n")
console = Path("guest-console.txt").read_text()
assert process.returncode == 0, "VM launcher failed"
assert "P1_GUEST_SUCCESS" in console and "P1_GUEST_FAILED" not in console, console[-4000:]
assert "--- SKIP:" not in console and "--- FAIL:" not in console, "Incomplete guest tests"
