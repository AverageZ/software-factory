import os
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch

import daemon_process
import rebuild


class DaemonOwnershipTests(unittest.TestCase):
    def test_macos_environment_ends_before_kernel_metadata(self):
        import ctypes

        raw = bytes(ctypes.c_int(3)) + (
            b"/checkout/factoryd\0\0/checkout/factoryd\0-data\0/data with spaces\0"
            b"HOME=/home\0CUSTOM=value with spaces\0\0"
            b"executable_file=kernel metadata\0HOME=/not-environment\0"
        )
        argv, environment = daemon_process.darwin_launch_fields(raw)
        self.assertEqual(argv, ["/checkout/factoryd", "-data", "/data with spaces"])
        self.assertEqual(environment, ["HOME=/home", "CUSTOM=value with spaces"])

    def test_control_socket_requires_owner_only_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "control.sock"
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
                listener.bind(str(path))
                os.chmod(path, 0o666)
                with self.assertRaisesRegex(RuntimeError, "unsafe or foreign"):
                    rebuild.socket_request({"control": str(path), "instance": "instance"}, "POST", "/restart")

    def test_identity_change_prevents_signal(self):
        launch = {"pid": 123, "identity": "old", "executable": "/checkout/factoryd", "directory": "/checkout",
                  "args": [], "environment": ["HOME=/home"], "legacy": True}
        current = dict(launch, identity="new")
        with patch.object(rebuild, "listener_pid", return_value=123), \
                patch.object(rebuild, "process_launch", return_value=current), \
                patch.object(rebuild.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "configuration changed"):
                rebuild.stop_daemon(launch, "127.0.0.1:9080")
            kill.assert_not_called()

    def test_listener_scan_preserves_custom_address(self):
        result = type("Result", (), {"returncode": 0, "stdout": "p123\nn127.0.0.1:9080\n", "stderr": ""})()
        launch = {"args": ["-addr", "127.0.0.1:9080"], "directory": "/checkout", "environment": ["HOME=/home"]}
        with patch.object(daemon_process.subprocess, "run", return_value=result), \
                patch.object(daemon_process, "process_launch", return_value=launch):
            self.assertEqual(daemon_process.checkout_listener(), "127.0.0.1:9080")

    def test_bad_unknown_flags_do_not_get_discarded(self):
        launch = {"args": ["-unknown", "custom"], "directory": "/checkout", "environment": ["HOME=/home"]}
        with self.assertRaisesRegex(RuntimeError, "Unknown daemon flag"):
            daemon_process.launch_settings(launch)


if __name__ == "__main__":
    unittest.main()
