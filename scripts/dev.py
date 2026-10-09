#!/usr/bin/env python3
"""Start the real local daemon and UI. Ctrl-C stops both process groups."""

import os
import subprocess
import sys

from common import ROOT, STATE, Processes, environment, require, require_port


def main():
    env = environment()
    require("npm", env)
    require("omp", env)
    for port in (8080, 5173):
        require_port(port)
    daemon = STATE / "bin" / "factoryd"
    if not daemon.is_file():
        raise RuntimeError("Run make build first.")
    with Processes(env) as stack:
        args = [str(daemon), "-addr", "127.0.0.1:8080"]
        if os.environ.get("FACTORY_DATA"):
            args.extend(["-data", os.environ["FACTORY_DATA"]])
        stack.start("Factory daemon", args)
        stack.wait_port(8080)
        stack.start("Factory UI", ["npm", "run", "dev", "--", "--host", "127.0.0.1", "--port", "5173", "--strictPort"], ROOT / "web")
        stack.wait_port(5173)
        print("Factory http://127.0.0.1:5173 | API http://127.0.0.1:8080", flush=True)
        print("Real execution only. Repository commands and agents run with your local permissions.", flush=True)
        stack.serve()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("Factory stopped.")
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
