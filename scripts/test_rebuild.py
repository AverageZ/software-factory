import os
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import daemon_process
import rebuild


class RebuildTests(unittest.TestCase):
    def launch(self, **extra):
        launch = {"pid": 123, "identity": "started yesterday", "executable": "/checkout/factoryd",
                  "directory": "/checkout", "args": ["-data", "custom data", "-web", "custom web"],
                  "environment": ["HOME=/custom-home", "CUSTOM=value with spaces"],
                  "address": "127.0.0.1:9080", "data": "/checkout/custom data", "web": "/checkout/custom web"}
        launch.update(extra)
        return launch

    def test_preserve_flags_paths_and_environment(self):
        settings = daemon_process.launch_settings(self.launch())
        self.assertEqual(settings, {"address": "127.0.0.1:8080", "data": "/checkout/custom data", "web": "/checkout/custom web"})
        settings = daemon_process.launch_settings(self.launch(args=["--data=~/durable", "-addr", "[::1]:9080", "-allow-public=false"]))
        self.assertEqual(settings["data"], "/custom-home/durable")
        self.assertEqual(settings["address"], "[::1]:9080")

    def test_unrelated_or_ambiguous_listener_is_not_stopped(self):
        result = subprocess.CompletedProcess([], 0, "p123\np456\n", "")
        with patch.object(daemon_process.subprocess, "run", return_value=result):
            with self.assertRaisesRegex(RuntimeError, "multiple process owners"):
                daemon_process.listener_pid(9080)
        with patch.object(rebuild, "listener_pid", return_value=123), \
                patch.object(rebuild, "process_launch", side_effect=RuntimeError("unrelated listener")), \
                patch.object(rebuild.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "unrelated listener"):
                rebuild.discover("127.0.0.1:9080")
            kill.assert_not_called()

    def test_managed_legacy_work_refuses_stop(self):
        launch = self.launch(legacy=True)
        runs = [{"status": "waiting", "nodes": {}}]
        projects = [{"id": "project", "repositories": [{"id": "repo"}]}]
        report = {"jobs": [], "revisions": [{"status": "working"}]}
        with patch.object(rebuild, "listener_pid", return_value=123), \
                patch.object(rebuild, "process_launch", return_value=launch), \
                patch.object(rebuild, "tcp_request", side_effect=[{"status": "ok"}, runs, projects, report]), \
                patch.object(rebuild.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "active managed work"):
                rebuild.stop_daemon(launch, launch["address"])
            kill.assert_not_called()

    def test_queued_legacy_issue_does_not_block_stop(self):
        projects = [{"id": "project", "repositories": [{"id": "repo"}]}]
        report = {"jobs": [{"status": "queued"}], "revisions": []}
        with patch.object(rebuild, "tcp_request", side_effect=[{"status": "ok"}, [], projects, report]):
            rebuild.ensure_legacy_idle("127.0.0.1:9080")

    def test_idle_legacy_stop_is_graceful(self):
        launch = self.launch(legacy=True)
        with patch.object(rebuild, "listener_pid", side_effect=[123, 123, None]), \
                patch.object(rebuild, "process_launch", return_value=launch), \
                patch.object(rebuild, "ensure_legacy_idle"), \
                patch.object(rebuild.os, "kill", side_effect=[None, ProcessLookupError()]) as kill:
            rebuild.stop_daemon(launch, launch["address"])
            self.assertEqual(kill.call_args_list[0].args, (123, signal.SIGTERM))
            self.assertEqual(kill.call_args_list[1].args, (123, 0))

    def test_modern_busy_control_never_sends_signal(self):
        ownership = self.launch(environment=["HOME=/exec-home", "CUSTOM=exec value"])
        launch = self.launch(control="/private/control.sock", instance="instance", ownership=ownership)
        with patch.object(rebuild, "listener_pid", return_value=123), \
                patch.object(rebuild, "process_launch", return_value=ownership), \
                patch.object(rebuild, "socket_request", side_effect=RuntimeError("active managed work")), \
                patch.object(rebuild.os, "kill") as kill:
            with self.assertRaisesRegex(RuntimeError, "active managed work"):
                rebuild.stop_daemon(launch, launch["address"])
            kill.assert_not_called()

    def test_build_failure_does_not_stop_or_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary)
            (state / "tmp").mkdir()
            with patch.object(rebuild, "STATE", state), \
                    patch.object(rebuild, "environment", return_value={}), \
                    patch.object(rebuild, "require"), \
                    patch.object(rebuild, "discover", return_value=self.launch()), \
                    patch.object(rebuild, "build_replacements", side_effect=RuntimeError("build failed")), \
                    patch.object(rebuild, "stop_daemon") as stop, \
                    patch.object(rebuild, "install_replacements") as install, \
                    patch.object(rebuild, "start_daemon") as start:
                with self.assertRaisesRegex(RuntimeError, "build failed"):
                    rebuild.main()
                stop.assert_not_called()
                install.assert_not_called()
                start.assert_not_called()

    def test_install_preserves_unrelated_custom_web_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            stage, web, state = root / "stage", root / "web", root / "state"
            (stage / "web").mkdir(parents=True)
            web.mkdir()
            (state / "bin").mkdir(parents=True)
            (stage / "web" / "index.html").write_text("new UI")
            (web / "user-file.txt").write_text("keep this")
            for name in ("factoryd", "factoryctl"):
                (stage / name).write_text("replacement")
                (state / "bin" / name).write_text("previous")
            with patch.object(rebuild, "STATE", state):
                rebuild.install_replacements(stage, web)
            self.assertEqual((web / "index.html").read_text(), "new UI")
            self.assertEqual((web / "user-file.txt").read_text(), "keep this")
            self.assertEqual((state / "bin" / "factoryd").read_text(), "replacement")


if __name__ == "__main__":
    unittest.main()
