"""Check real terminal ownership, which Textual's headless pilot cannot exercise."""

import importlib.util
import json
import os
import select
import shutil
import signal
import sys
import time
from pathlib import Path

import pytest

pty = pytest.importorskip("pty")
pytestmark = pytest.mark.integration
ROOT = Path(__file__).resolve().parents[2]


@pytest.mark.parametrize("adapter", ["python", "go"])
def test_launch_preserves_terminal_foreground_and_input(tmp_path, adapter):
    if adapter == "python":
        if importlib.util.find_spec("debugpy") is None:
            pytest.skip("debugpy is not installed")
        program = tmp_path / "main.py"
        program.write_text("value = 41\nprint(value)\n")
        arguments = {"program": str(program)}
        line = 2
        executable = {"python": sys.executable}
    else:
        dlv = shutil.which("dlv")
        local_dlv = ROOT / ".tools/bin/dlv"
        if dlv is None and local_dlv.is_file():
            dlv = str(local_dlv)
        if dlv is None or shutil.which("go") is None:
            pytest.skip("Go and Delve are required")
        program = tmp_path / "main.go"
        program.write_text('package main\nimport "fmt"\nfunc main() {\nfmt.Println(41)\n}\n')
        (tmp_path / "go.mod").write_text("module example.com/terminal-test\n\ngo 1.22\n")
        arguments = {"program": str(tmp_path), "cwd": str(tmp_path), "mode": "debug"}
        line = 4
        executable = {"dlv": dlv}
    config = tmp_path / "session.json"
    config.write_text(
        json.dumps(
            {
                "adapter": adapter,
                "request": "launch",
                "arguments": arguments,
                "breakpoints": {str(program): [line]},
                "startupTimeout": 20,
                **executable,
            }
        )
    )
    pid, terminal = pty.fork()
    if pid == 0:
        os.execv(
            sys.executable,
            [
                sys.executable,
                str(ROOT / "tests/fixtures/terminal_session.py"),
                str(config),
            ],
        )

    output = bytearray()
    status = None
    sent_input = False
    deadline = time.monotonic() + 30
    try:
        while time.monotonic() < deadline:
            readable, _, _ = select.select([terminal], [], [], 0.05)
            if readable:
                try:
                    chunk = os.read(terminal, 65536)
                except OSError:
                    chunk = b""
                output.extend(chunk)
            if not sent_input and b"TERMINAL_READY" in output:
                os.write(terminal, b"keyboard-and-mouse\n")
                sent_input = True
            finished, observed = os.waitpid(pid, os.WNOHANG | os.WUNTRACED)
            if finished:
                status = observed
                break
        transcript = output.decode(errors="replace")
        assert status is not None, f"Terminal probe timed out:\n{transcript}"
        assert os.WIFEXITED(status) and os.WEXITSTATUS(status) == 0, transcript
        assert b"TERMINAL_INPUT_OK" in output, transcript
    finally:
        if status is None or os.WIFSTOPPED(status):
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        os.close(terminal)
