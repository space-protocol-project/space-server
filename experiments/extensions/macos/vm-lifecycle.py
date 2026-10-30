"""CI: SIGKILL launcher/VMM, исчезновение процессов, холодный запуск и учёт памяти."""
import json
import os
from pathlib import Path
import signal
import subprocess
import time

OUT = Path("vm-lifecycle")
OUT.mkdir(exist_ok=True)
SERVICE = "com.apple.Virtualization.VirtualMachine"
records = []


def processes():
    raw = subprocess.check_output(["ps", "-axo", "pid=,ppid=,comm="], text=True)
    result = {}
    for line in raw.splitlines():
        parts = line.strip().split(None, 2)
        if len(parts) == 3:
            result[int(parts[0])] = {"ppid": int(parts[1]), "command": parts[2]}
    return result


def services():
    return {pid: item for pid, item in processes().items() if SERVICE in item["command"]}


def usage(pid):
    raw = subprocess.check_output(["sudo", "-n", "./process-memory", str(pid)], text=True)
    return json.loads(raw)


def system_snapshot(name):
    raw = subprocess.check_output(["vm_stat"], text=True)
    (OUT / (name + "-vm-stat.txt")).write_text(raw)


def sample(launcher, record):
    table = services()
    pids = set(table)
    if launcher.poll() is None:
        pids.add(launcher.pid)
    items = []
    for pid in sorted(pids):
        item = usage(pid)
        item.update(table.get(pid, {"command": "guest-launcher"}))
        items.append(item)
    valid = [item for item in items if item["errno"] == 0]
    record["samples"].append({
        "seconds": round(time.monotonic() - record["started"], 3),
        "processes": items,
        "resident_sum_bytes": sum(item["resident_bytes"] for item in valid),
        "footprint_sum_bytes": sum(item["footprint_bytes"] for item in valid),
    })
    return table


def console_text(path):
    return path.read_text(errors="replace") if path.exists() else ""


def await_condition(launcher, record, predicate, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        table = sample(launcher, record)
        if predicate(table):
            return table
        if launcher.poll() is not None:
            raise RuntimeError("Launcher exited before expected guest state")
        time.sleep(1)
    raise TimeoutError("Guest state deadline")


def drain(launcher, record):
    started = time.monotonic()
    while time.monotonic() - started < 60:
        table = sample(launcher, record)
        if launcher.poll() is not None and not table:
            record["cleanup_seconds"] = round(time.monotonic() - started, 3)
            return
        time.sleep(1)
    raise TimeoutError("VM processes remain after shutdown")


def kill_identified(pid, identity):
    current = usage(pid)
    assert current["errno"] == 0 and current["start_abstime"] == identity, "PID identity changed"
    subprocess.run(["sudo", "-n", "kill", "-KILL", str(pid)], check=True)


def run_case(name, target=None):
    assert not services(), "Runner already has a VM service; attribution would be ambiguous"
    system_snapshot(name + "-before")
    console = OUT / (name + "-console.txt")
    record = {"name": name, "target": target, "samples": [], "started": time.monotonic()}
    records.append(record)
    command = ["./guest-launcher", "vm-image/vmlinuz", "vm-image/initramfs.gz", str(console)]
    if target:
        command.append("hold")
    with (OUT / (name + "-launcher.log")).open("w") as log:
        launcher = subprocess.Popen(command, stdout=log, stderr=log)
        record["launcher_pid"] = launcher.pid
        try:
            if target:
                table = await_condition(launcher, record, lambda table: console_text(console).count("P1_VM_HEARTBEAT") >= 3 and len(table) == 1, 180)
                system_snapshot(name + "-loaded")
                victim = launcher.pid if target == "launcher" else next(iter(table))
                identity = usage(victim)
                assert identity["errno"] == 0
                record["victim"] = identity
                record["services_at_kill"] = table
                record["heartbeat_before_kill"] = console_text(console).count("P1_VM_HEARTBEAT")
                kill_identified(victim, identity["start_abstime"])
            else:
                deadline = time.monotonic() + 260
                while launcher.poll() is None and time.monotonic() < deadline:
                    sample(launcher, record)
                    time.sleep(1)
                assert launcher.poll() == 0, "Healthy restart failed"
                text = console_text(console)
                assert "P1_GUEST_SUCCESS" in text and "P1_GUEST_FAILED" not in text
                assert "--- FAIL:" not in text
                skips = [line for line in text.splitlines() if "--- SKIP:" in line]
                assert all(line.startswith("--- SKIP: TestIsolationSupervisorHelper ") for line in skips)
            drain(launcher, record)
            before = console_text(console)
            time.sleep(3)
            assert console_text(console) == before, "Guest still writes after VM cleanup"
            if target:
                assert "P1_GUEST_SUCCESS" not in before
                assert launcher.returncode != 0, "Crash did not reach launcher"
            record["exit_code"] = launcher.returncode
            record["success"] = True
            system_snapshot(name + "-after")
        finally:
            if launcher.poll() is None:
                launcher.kill()
            launcher.wait()
            record["elapsed_seconds"] = round(time.monotonic() - record.pop("started"), 3)


try:
    run_case("baseline")
    run_case("kill-launcher", "launcher")
    run_case("restart-after-launcher")
    run_case("kill-vmm", "service")
    run_case("restart-after-vmm")
finally:
    (OUT / "results.json").write_text(json.dumps({
        "guest_ram_bytes": 1073741824,
        "guest_pressure_bytes": 268435456,
        "aggregate_hard_limit_verified": False,
        "measurement_scope": "launcher plus discovered Virtualization.VirtualMachine services; non-atomic process sums, excludes unattributed kernel memory",
        "cases": records,
    }, indent=2) + "\n")
