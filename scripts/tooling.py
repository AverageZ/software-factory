#!/usr/bin/env python3
"""Small local build commands; no downloaded service stack or fake adapters."""

import argparse
import subprocess
import sys

from common import ROOT, STATE, environment, require, run


def go_files():
    return sorted(str(path.relative_to(ROOT)) for base in (ROOT / "cmd", ROOT / "internal")
                  for path in base.rglob("*.go") if not path.is_symlink())


def formatting(env, fix):
    files = go_files()
    if fix:
        run(["gofmt", "-w", *files], env)
    else:
        result = subprocess.check_output(["gofmt", "-l", *files], cwd=ROOT, env=env, text=True)
        if result.strip():
            raise RuntimeError("Run make fmt:\n" + result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("bootstrap", "build", "test", "lint", "fmt", "check-fmt", "pre-commit"))
    command = parser.parse_args().command
    env = environment()
    require("go", env)
    if command == "bootstrap":
        for binary in ("node", "npm", "git", "omp"):
            require(binary, env)
        run(["go", "mod", "download"], env)
        run(["npm", "ci", "--no-audit", "--no-fund"], env, ROOT / "web")
    elif command == "build":
        for name in ("factoryd", "factoryctl"):
            run(["go", "build", "-o", str(STATE / "bin" / name), "./cmd/" + name], env)
        run(["npm", "run", "build"], env, ROOT / "web")
    elif command == "test":
        run(["go", "test", "-race", "./..."], env)
        run(["npm", "run", "build"], env, ROOT / "web")
    elif command in ("fmt", "check-fmt"):
        formatting(env, command == "fmt")
    else:
        formatting(env, False)
        run(["go", "vet", "./..."], env)
        run(["npm", "run", "lint"], env, ROOT / "web")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        sys.exit(130)
