"""Repository-local environment and foreground process supervision (Python 3.10+)."""

import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / ".factory-data"


def local_path(path):
    path = Path(path)
    if not path.is_absolute():
        path = ROOT / path
    if STATE.is_symlink() or not path.resolve().is_relative_to(STATE.resolve()) or not STATE.resolve().is_relative_to(ROOT):
        raise RuntimeError(f"Refusing state outside {ROOT / '.factory-data'}: {path}")
    return path


def environment():
    # Keep the user's HOME, PATH, XDG settings and auth intact for the real harness.
    env = os.environ.copy()
    for key, directory in {
        "GOCACHE": "go-cache", "GOMODCACHE": "go-mod", "GOPATH": "go-path",
        "GOBIN": "bin", "TMPDIR": "tmp", "NPM_CONFIG_CACHE": "npm-cache",
    }.items():
        path = local_path(STATE / directory)
        path.mkdir(parents=True, exist_ok=True)
        env[key] = str(path)
    env["TMP"] = env["TMPDIR"]
    env["TEMP"] = env["TMPDIR"]
    env["npm_config_cache"] = env["NPM_CONFIG_CACHE"]
    env["GOENV"] = "off"
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    env["PATH"] = env.get("PATH", os.defpath)
    return env


def require(program, env):
    executable = shutil.which(program, path=env["PATH"])
    if executable is None:
        raise RuntimeError(f"Missing prerequisite {program!r}; install it, then run make bootstrap.")
    return executable


def run(args, env, cwd=ROOT, **kwargs):
    print("+ " + " ".join(str(arg) for arg in args), flush=True)
    return subprocess.run(args, cwd=cwd, env=env, check=True, **kwargs)


def require_port(port):
    with socket.socket() as sock:
        try:
            sock.bind(("127.0.0.1", port))
        except OSError as error:
            raise RuntimeError(f"Loopback port {port} is occupied; stop its owner before starting this stack.") from error


class Processes:
    """Children are separate process groups, all reaped on error, exit, or Ctrl-C."""

    def __init__(self, env):
        self.env = env
        self.children = []
        self.handlers = {}

    def __enter__(self):
        def interrupt(_signum, _frame):
            raise KeyboardInterrupt
        for signum in (signal.SIGINT, signal.SIGTERM):
            self.handlers[signum] = signal.signal(signum, interrupt)
        return self

    def start(self, name, args, cwd=ROOT):
        print(f"Starting {name}: " + " ".join(str(arg) for arg in args), flush=True)
        process = subprocess.Popen(args, cwd=cwd, env=self.env, start_new_session=True)
        self.children.append((name, process))
        return process

    def check(self):
        for name, process in self.children:
            code = process.poll()
            if code is not None:
                raise RuntimeError(f"{name} exited unexpectedly (status {code}).")

    def wait_port(self, port, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.check()
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                    return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError(f"Timed out waiting for loopback port {port}.")

    def serve(self):
        while True:
            self.check()
            time.sleep(0.25)

    def __exit__(self, _kind, _value, _traceback):
        for signum in self.handlers:
            signal.signal(signum, signal.SIG_IGN)
        try:
            for _name, process in reversed(self.children):
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            deadline = time.monotonic() + 8
            for _name, process in reversed(self.children):
                try:
                    process.wait(timeout=max(0, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    pass
            # Kill remaining descendants even if their process-group leader already exited.
            for _name, process in reversed(self.children):
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait()
        finally:
            for signum, handler in self.handlers.items():
                signal.signal(signum, handler)
