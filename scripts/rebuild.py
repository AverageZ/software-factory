#!/usr/bin/env python3
"""Build complete replacements, then safely restart the local Factory daemon."""

import fcntl
import http.client
import ipaddress
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote

from common import ROOT, STATE, environment, require, run
from daemon_process import checkout_listener, launch_settings, listener_pid, process_launch


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=10)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def endpoint(address):
    host, separator, port = address.rpartition(":")
    host = host.strip("[]")
    if not separator or not port.isdigit() or not 0 < int(port) < 65536:
        raise RuntimeError(f"Invalid FACTORY_ADDR: {address}")
    if host in ("", "0.0.0.0"):
        host = "127.0.0.1"
    elif host == "::":
        host = "::1"
    elif host != "localhost":
        try:
            if not ipaddress.ip_address(host).is_loopback:
                raise ValueError()
        except ValueError as error:
            raise RuntimeError("Rebuild requires a loopback or wildcard listen address.") from error
    return host, int(port)


def request(connection, method, path, headers=None):
    try:
        connection.request(method, path, headers=headers or {})
        response = connection.getresponse()
        payload = response.read(8 << 20)
        try:
            value = json.loads(payload)
        except (ValueError, UnicodeDecodeError) as error:
            raise RuntimeError(f"Listener returned invalid JSON for {path}; refusing restart.") from error
        if response.status != 200:
            raise RuntimeError(f"{path}: {value.get('error', value) if isinstance(value, dict) else value}")
        return value
    finally:
        connection.close()


def tcp_request(address, path):
    host, port = endpoint(address)
    return request(http.client.HTTPConnection(host, port, timeout=5), "GET", path)


def socket_request(info, method, path):
    control = Path(info["control"])
    for target, kind in ((control.parent, stat.S_ISDIR), (control, stat.S_ISSOCK)):
        metadata = target.lstat()
        if metadata.st_uid != os.getuid() or metadata.st_mode & 0o077 or not kind(metadata.st_mode):
            raise RuntimeError("Refusing unsafe or foreign daemon control socket.")
    return request(UnixHTTPConnection(str(control)), method, path,
                   {"X-Factory-Instance": info["instance"]})


def ensure_legacy_idle(address):
    health = tcp_request(address, "/api/health")
    if health != {"status": "ok"}:
        raise RuntimeError("Daemon is not healthy; refusing legacy restart.")
    runs = tcp_request(address, "/api/runs")
    projects = tcp_request(address, "/api/projects")
    if not isinstance(runs, list) or not isinstance(projects, list):
        raise RuntimeError("Unknown daemon API; refusing legacy restart.")
    for execution in runs:
        if execution.get("status") == "running" or any(
                node.get("status") == "running" for node in execution.get("nodes", {}).values()):
            raise RuntimeError("Daemon has active workflow work; rebuild after it finishes.")
    for project in projects:
        for repository in project["repositories"] or []:
            path = f"/api/projects/{quote(project['id'], safe='')}/repositories/{quote(repository['id'], safe='')}/intake"
            report = tcp_request(address, path)
            synchronizations = report.get("synchronizations", [])
            if (not isinstance(report.get("jobs"), list)
                    or not isinstance(report.get("revisions"), list)
                    or not isinstance(synchronizations, list)):
                raise RuntimeError("Unknown intake API; refusing legacy restart.")
            # A queued issue has not allocated a workspace or started a run, so
            # stopping the legacy daemon cannot interrupt it. Managed PR jobs use
            # a stricter restart boundary because admission owns a pull request.
            for job in report["jobs"]:
                if job.get("status") in ("working", "publishing"):
                    raise RuntimeError("Daemon has active managed work; rebuild after it finishes.")
            for revision in [*report["revisions"], *synchronizations]:
                if revision.get("status") in ("queued", "working", "publishing"):
                    raise RuntimeError("Daemon has active managed work; rebuild after it finishes.")


def discover(address):
    _host, port = endpoint(address)
    pid = listener_pid(port)
    if pid is None:
        return None
    launch = process_launch(pid)
    try:
        info = tcp_request(address, "/api/daemon")
    except RuntimeError:
        # Old daemons serve the SPA at unknown paths. Validate their actual API
        # and executable instead of assuming that any port owner is Factory.
        ensure_legacy_idle(address)
        launch.update(launch_settings(launch))
        if launch["address"].endswith(":0"):
            launch["address"] = launch["address"].rpartition(":")[0] + ":" + str(port)
        launch["legacy"] = True
        return launch
    if not isinstance(info, dict) or info.get("protocol") != "factoryd-restart-v1" or info.get("pid") != pid:
        raise RuntimeError("Unknown daemon identity; refusing restart.")
    controlled = socket_request(info, "GET", "/info")
    for field in ("protocol", "instance", "pid", "control", "address", "data", "web"):
        if controlled.get(field) != info.get(field):
            raise RuntimeError("Daemon identity changed during discovery; refusing restart.")
    controlled["identity"] = launch["identity"]
    controlled["executable"] = launch["executable"]
    # The OS records exec-time values; Go can change its effective environment.
    # Check ownership against the same source and restart with the daemon's values.
    controlled["ownership"] = launch
    return controlled


def build_replacements(stage, env):
    for name in ("factoryd", "factoryctl"):
        run(["go", "build", "-o", str(stage / name), "./cmd/" + name], env)
    run(["npm", "run", "build", "--", "--outDir", str(stage / "web"), "--emptyOutDir"], env, ROOT / "web")


def install_replacements(stage, web):
    web = Path(web)
    web.mkdir(parents=True, exist_ok=True)
    web = web.resolve()
    # Replace only build outputs, not the user's entire custom web directory.
    for source in sorted((stage / "web").rglob("*")):
        if not source.is_file():
            continue
        target = web / source.relative_to(stage / "web")
        target.parent.mkdir(parents=True, exist_ok=True)
        if not target.parent.resolve().is_relative_to(web):
            raise RuntimeError(f"Refusing web output through an external symlink: {target}")
        with tempfile.NamedTemporaryFile(dir=target.parent, prefix=".factory-build-", delete=False) as output:
            temporary = Path(output.name)
            try:
                with source.open("rb") as input_file:
                    shutil.copyfileobj(input_file, output)
                output.flush()
                os.fchmod(output.fileno(), 0o644)
            except BaseException:
                temporary.unlink(missing_ok=True)
                raise
        try:
            os.replace(temporary, target)
        finally:
            temporary.unlink(missing_ok=True)
    for name in ("factoryd", "factoryctl"):
        os.replace(stage / name, STATE / "bin" / name)


def same_process(current, launch):
    launch = launch.get("ownership", launch)
    return (Path(current["directory"]).resolve() == Path(launch["directory"]).resolve()
            and all(current.get(field) == launch.get(field)
                    for field in ("pid", "identity", "executable", "args", "environment")))


def stop_daemon(launch, address):
    _host, port = endpoint(address)
    if listener_pid(port) != launch["pid"]:
        raise RuntimeError("Listener ownership changed; refusing restart.")
    current = process_launch(launch["pid"])
    if not same_process(current, launch):
        raise RuntimeError("Daemon launch configuration changed; refusing restart.")
    if launch.get("legacy"):
        print("Upgrading legacy daemon: idle API checks cannot prevent a concurrent new admission.", flush=True)
        # Repeat all public work checks immediately before sending TERM. A legacy
        # daemon has no atomic admission gate; newer daemons use /restart below.
        ensure_legacy_idle(address)
        if listener_pid(port) != launch["pid"] or not same_process(process_launch(launch["pid"]), launch):
            raise RuntimeError("Daemon ownership changed before stop; refusing restart.")
        os.kill(launch["pid"], signal.SIGTERM)
    else:
        socket_request(launch, "POST", "/restart")
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        owner = listener_pid(port)
        if owner is not None and owner != launch["pid"]:
            raise RuntimeError("Another process acquired the daemon port; not starting replacement.")
        if owner is None and (launch.get("legacy") or not Path(launch["control"]).exists()):
            # On legacy daemons TCP shutdown happens before engine.Close. Wait
            # for process exit too, so the replacement cannot race the DB lock.
            if launch.get("legacy"):
                try:
                    os.kill(launch["pid"], 0)
                except ProcessLookupError:
                    return
            else:
                return
        time.sleep(0.2)
    raise RuntimeError("Daemon did not stop within 30 seconds; no force kill or replacement start was attempted.")


def show_startup_log(path):
    with path.open(errors="replace") as log:
        log.seek(0, os.SEEK_END)
        log.seek(max(0, log.tell() - 16000))
        print(log.read(), file=sys.stderr)


def start_daemon(launch):
    data = Path(launch["data"])
    data.mkdir(parents=True, exist_ok=True, mode=0o700)
    log_path = data / "factoryd.log"
    env = dict(item.split("=", 1) for item in launch["environment"] if "=" in item)
    args = [str(STATE / "bin" / "factoryd"), *launch["args"], "-addr", launch["address"]]
    descriptor = os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(descriptor, "ab") as log:
        process = subprocess.Popen(args, cwd=launch["directory"], env=env, stdin=subprocess.DEVNULL,
                                   stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        code = process.poll()
        if code is not None:
            show_startup_log(log_path)
            raise RuntimeError(f"Replacement daemon exited with status {code}; log: {log_path}")
        try:
            info = tcp_request(launch["address"], "/api/daemon")
            if info.get("protocol") == "factoryd-restart-v1" and info.get("pid") == process.pid:
                if tcp_request(launch["address"], "/api/health") == {"status": "ok"}:
                    print(f"Factory http://{launch['address']} | PID {process.pid} | log {log_path}", flush=True)
                    return
        except (OSError, RuntimeError, http.client.HTTPException):
            pass
        time.sleep(0.2)
    show_startup_log(log_path)
    raise RuntimeError(f"No healthy replacement within 30 seconds (PID {process.pid}); log: {log_path}. "
                       "Process left running to avoid interrupting admitted work.")


def main():
    env = environment()
    for binary in ("go", "npm", "lsof"):
        require(binary, env)
    address = os.environ.get("FACTORY_ADDR", "127.0.0.1:8080")
    # Serialize commands before discovery, staging and the complete stop/start.
    with (STATE / "rebuild.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise RuntimeError("Another make rebuild is in progress.") from error
        launch = discover(address)
        if launch is None and "FACTORY_ADDR" not in os.environ:
            custom_address = checkout_listener()
            if custom_address is not None:
                address = custom_address
                launch = discover(address)
        if launch is None:
            data = str(Path(os.environ.get("FACTORY_DATA", str(Path.home() / ".factory"))).expanduser().resolve())
            web = str(Path(os.environ.get("FACTORY_WEB", str(ROOT / "web" / "dist"))).expanduser().resolve())
            launch = {"data": data, "web": web, "address": address, "directory": str(ROOT),
                      "args": ["-data", data, "-web", web],
                      "environment": [key + "=" + value for key, value in os.environ.items()]}
        # No current process or served web files change until all builds succeed.
        with tempfile.TemporaryDirectory(prefix="rebuild-", dir=STATE / "tmp") as temporary:
            stage = Path(temporary)
            build_replacements(stage, env)
            if "pid" in launch:
                stop_daemon(launch, address)
            elif listener_pid(endpoint(address)[1]) is not None:
                raise RuntimeError("A process acquired the requested port during build; refusing startup.")
            install_replacements(stage, launch["web"])
            start_daemon(launch)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError, http.client.HTTPException) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        sys.exit(130)
