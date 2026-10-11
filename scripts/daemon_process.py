"""Inspect local daemon ownership without guessing a PID from its name."""

import ctypes
import os
from pathlib import Path
import subprocess
import sys

from common import STATE


def listener_pid(port):
    result = subprocess.run(["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN", "-Fp"],
                            capture_output=True, text=True)
    if result.returncode not in (0, 1) or (result.returncode == 1 and result.stderr.strip()):
        raise RuntimeError("Cannot inspect listener ownership: " + result.stderr.strip())
    pids = {int(line[1:]) for line in result.stdout.splitlines() if line.startswith("p")}
    if len(pids) > 1:
        raise RuntimeError(f"Refusing multiple process owners on port {port}.")
    return next(iter(pids), None)


def checkout_listener():
    """Find a custom-address daemon from this checkout, never by its name."""
    result = subprocess.run(["lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn"],
                            capture_output=True, text=True)
    if result.returncode not in (0, 1) or (result.returncode == 1 and result.stderr.strip()):
        raise RuntimeError("Cannot inspect local listeners: " + result.stderr.strip())
    addresses = {}
    pid = None
    for line in result.stdout.splitlines():
        if line.startswith("p"):
            pid = int(line[1:])
        elif line.startswith("n") and pid is not None:
            try:
                launch = process_launch(pid)
            except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError):
                continue
            settings = launch_settings(launch)
            address = settings["address"]
            if address.endswith(":0"):
                address = address.rpartition(":")[0] + ":" + line[1:].rpartition(":")[2]
            addresses[pid] = address
    if len(addresses) > 1:
        raise RuntimeError("Multiple checkout daemons are running; select one with FACTORY_ADDR.")
    return next(iter(addresses.values()), None)


def darwin_launch_fields(raw):
    argc = ctypes.c_int.from_buffer_copy(raw[:ctypes.sizeof(ctypes.c_int)]).value
    payload = raw[ctypes.sizeof(ctypes.c_int):]
    end = payload.find(b"\0")
    if argc < 1 or end < 0:
        raise RuntimeError("Invalid daemon launch metadata.")
    fields = payload[end:].lstrip(b"\0").split(b"\0")
    argv = [os.fsdecode(item) for item in fields[:argc]]
    environment = []
    for item in fields[argc:]:
        if not item:
            break
        environment.append(os.fsdecode(item))
    return argv, environment


def process_launch(pid):
    if sys.platform == "linux":
        base = Path("/proc") / str(pid)
        if base.stat().st_uid != os.getuid():
            raise RuntimeError("Refusing a daemon owned by another user.")
        executable = str((base / "exe").resolve(strict=True))
        directory = str((base / "cwd").resolve(strict=True))
        argv = [os.fsdecode(item) for item in (base / "cmdline").read_bytes().split(b"\0")[:-1]]
        environment = [os.fsdecode(item) for item in (base / "environ").read_bytes().split(b"\0") if item]
        identity = (base / "stat").read_text().rsplit(")", 1)[1].split()[19]
    elif sys.platform == "darwin":
        identity = subprocess.check_output(["ps", "-p", str(pid), "-o", "uid=,lstart="], text=True).strip()
        if not identity or int(identity.split()[0]) != os.getuid():
            raise RuntimeError("Refusing a daemon owned by another user.")
        libproc = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        buffer = ctypes.create_string_buffer(4096)
        if libproc.proc_pidpath(pid, buffer, len(buffer)) <= 0:
            raise RuntimeError("Cannot read daemon executable path.")
        executable = os.fsdecode(buffer.value)
        libc = ctypes.CDLL(None, use_errno=True)
        mib = (ctypes.c_int * 3)(1, 49, pid)  # CTL_KERN, KERN_PROCARGS2
        size = ctypes.c_size_t(0)
        if libc.sysctl(mib, 3, None, ctypes.byref(size), None, 0) != 0:
            raise RuntimeError("Cannot read daemon launch arguments.")
        raw = ctypes.create_string_buffer(size.value)
        if libc.sysctl(mib, 3, raw, ctypes.byref(size), None, 0) != 0:
            raise RuntimeError("Cannot read daemon launch environment.")
        argv, environment = darwin_launch_fields(raw.raw[:size.value])
        cwd = subprocess.check_output(["lsof", "-a", "-p", str(pid), "-d", "cwd", "-Fn"], text=True)
        paths = [line[1:] for line in cwd.splitlines() if line.startswith("n")]
        if len(paths) != 1:
            raise RuntimeError("Cannot determine daemon working directory.")
        directory = paths[0]
    else:
        raise RuntimeError("Automatic legacy restart supports macOS and Linux only.")
    # A same-name executable, or another checkout's daemon, is not ours to stop.
    if Path(executable).resolve() != (STATE / "bin" / "factoryd").resolve():
        raise RuntimeError(f"Refusing unrelated listener executable: {executable}")
    if not argv or not environment or not Path(directory).is_dir():
        raise RuntimeError("Incomplete daemon launch configuration; refusing restart.")
    return {"pid": pid, "executable": executable, "identity": identity,
            "directory": directory, "args": argv[1:], "environment": environment}


def launch_settings(launch):
    env = dict(item.split("=", 1) for item in launch["environment"] if "=" in item)
    data = str(Path(env.get("HOME", str(Path.home()))) / ".factory")
    web, address = "web/dist", "127.0.0.1:8080"
    args = launch["args"]
    index = 0
    while index < len(args):
        flag, separator, value = args[index].lstrip("-").partition("=")
        if flag in ("data", "web", "addr"):
            if not separator:
                index += 1
                if index >= len(args):
                    raise RuntimeError("Incomplete daemon flags.")
                value = args[index]
            if flag == "data":
                data = value
            elif flag == "web":
                web = value
            else:
                address = value
        elif flag != "allow-public":
            raise RuntimeError(f"Unknown daemon flag; refusing restart: {args[index]}")
        index += 1
    if data.startswith("~/"):
        data = str(Path(env.get("HOME", str(Path.home()))) / data[2:])
    directory = Path(launch["directory"])
    return {"data": str((directory / data).resolve()), "web": str((directory / web).resolve()), "address": address}
