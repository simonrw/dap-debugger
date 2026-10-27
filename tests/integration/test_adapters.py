"""Exercise the public session API against real loopback debugger processes."""

import asyncio
import importlib.util
import os
import shutil
import signal
import socket
import subprocess
import sys
import time
from contextlib import contextmanager
from pathlib import Path

import pytest

from dap_debugger.config import SessionConfig
from dap_debugger.session import DAPError, DebugSession

pytestmark = pytest.mark.integration


PYTHON_SOURCE = """import os
from pathlib import Path
import sys
import time


def work():
    value = 41
    answer = value + 1  # BREAKPOINT
    answer += 1
    return answer


heartbeat, finish = map(Path, sys.argv[1:])
iteration = 0
while not finish.exists():
    work()
    iteration += 1
    heartbeat.write_text(f"{os.getpid()}:{iteration}")
    time.sleep(0.02)
"""

GO_SOURCE = """package main

import (
    "fmt"
    "os"
    "time"
)

func work() int {
    value := 41
    answer := value + 1 // BREAKPOINT
    answer++
    return answer
}

func main() {
    for iteration := 1; ; iteration++ {
        if _, err := os.Stat(os.Args[2]); err == nil {
            return
        }
        work()
        content := fmt.Sprintf("%d:%d", os.Getpid(), iteration)
        if err := os.WriteFile(os.Args[1], []byte(content), 0600); err != nil {
            panic(err)
        }
        time.Sleep(20 * time.Millisecond)
    }
}
"""

PYTHON_LISTENER = """import debugpy
from pathlib import Path
import runpy
import sys

port, ready, program, *arguments = sys.argv[1:]
debugpy.listen(("127.0.0.1", int(port)))
Path(ready).write_text("listening")
sys.argv = [program, *arguments]
runpy.run_path(program, run_name="__main__")
"""


@pytest.fixture
def python_adapter():
    if importlib.util.find_spec("debugpy") is None:
        pytest.skip(f"debugpy is not installed in {sys.executable}")
    return sys.executable


@pytest.fixture
def go_adapter():
    go = shutil.which("go")
    if go is None:
        pytest.skip("Go executable 'go' is not on PATH")
    local_dlv = Path(__file__).resolve().parents[2] / ".tools" / "bin" / "dlv"
    dlv = shutil.which("dlv")
    if dlv is None and local_dlv.is_file():
        dlv = str(local_dlv)
    if dlv is None:
        pytest.skip("Delve executable 'dlv' is not on PATH or in .tools/bin")
    return go, dlv


def available_port():
    try:
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            return listener.getsockname()[1]
    except PermissionError as error:
        pytest.skip(f"OS forbids binding a loopback debugger listener: {error}")


def skip_debugging_restriction(message):
    restrictions = [
        "operation not permitted",
        "could not get task for pid",
        "could not acquire mach task port",
        "ptrace: permission denied",
        "ptrace_scope",
        "not allowed to attach to process",
        "failed to get connection from debugserver",
    ]
    if any(reason in message.lower() for reason in restrictions):
        pytest.skip(f"OS debugging restriction on {sys.platform}: {message.strip()}")


@contextmanager
def external_process(argv, directory):
    log_path = directory / "adapter.log"
    with log_path.open("w") as log:
        process = subprocess.Popen(
            argv,
            cwd=directory,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=os.name == "posix",
        )
        try:
            yield process, log_path
        finally:
            # Only signal the process group created by this fixture, never a DAP PID.
            if os.name == "posix":
                for sig in [signal.SIGTERM, signal.SIGKILL]:
                    try:
                        os.killpg(process.pid, sig)
                    except ProcessLookupError:
                        break
                    try:
                        process.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        pass
            elif process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
            process.wait(timeout=3)


def wait_for_fixture(predicate, process, log_path, *, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        diagnostics = log_path.read_text()
        skip_debugging_restriction(diagnostics)
        if process.poll() is not None:
            pytest.fail(f"Debugger fixture exited ({process.returncode}):\n{diagnostics}")
        if predicate():
            return
        time.sleep(0.05)
    pytest.fail(f"Debugger fixture was not ready after {timeout}s:\n{log_path.read_text()}")


def make_target(directory, adapter, *, mapped=False):
    remote = directory / "target-source"
    remote.mkdir()
    name = "main.py" if adapter == "python" else "main.go"
    source = PYTHON_SOURCE if adapter == "python" else GO_SOURCE
    program = remote / name
    program.write_text(source)
    if adapter == "go":
        (remote / "go.mod").write_text("module example.com/debug-target\n\ngo 1.22\n")
    local = remote
    if mapped:
        local = directory / "local-source"
        local.mkdir()
        (local / name).write_text(source)
    line = next(index for index, text in enumerate(source.splitlines(), 1) if "BREAKPOINT" in text)
    return program, local / name, line


def config_for(directory, adapter, request, source, line, **options):
    return SessionConfig.from_dict(
        {
            "adapter": adapter,
            "request": request,
            "breakpoints": {str(source): [line]},
            "requestTimeout": 10,
            "startupTimeout": 30,
            **options,
        },
        base_dir=directory,
    )


async def event_named(session, name):
    async with asyncio.timeout(15):
        while True:
            event = await session.next_event()
            if event["event"] == name:
                return event["body"]
            if event["event"] == "sessionError":
                pytest.fail(f"Session error waiting for {name}: {event['body']}")
            if event["event"] == "terminated" or (
                event["event"] == "sessionState" and event["body"]["state"] in ["failed", "closed"]
            ):
                pytest.fail(f"Session ended waiting for {name}: {event}")


async def inspect_and_step(session, source, line):
    stopped = await event_named(session, "stopped")
    assert stopped["reason"] == "breakpoint"
    assert session.state == "stopped"
    threads = (await session.request("threads"))["threads"]
    thread_id = stopped.get("threadId", threads[0]["id"])
    assert thread_id in [thread["id"] for thread in threads]
    frames = (await session.request("stackTrace", {"threadId": thread_id}))["stackFrames"]
    frame = frames[0]
    assert Path(frame["source"]["path"]).resolve() == source.resolve()
    assert frame["line"] == line
    scopes = (await session.request("scopes", {"frameId": frame["id"]}))["scopes"]
    variables = []
    for scope in scopes:
        if scope["variablesReference"] > 0 and not scope.get("expensive", False):
            variables.extend(
                (
                    await session.request(
                        "variables", {"variablesReference": scope["variablesReference"]}
                    )
                )["variables"]
            )
    assert any(variable["name"] == "value" and variable["value"] == "41" for variable in variables)
    result = await session.request(
        "evaluate", {"expression": "value + 1", "frameId": frame["id"], "context": "repl"}
    )
    assert result["result"] == "42"
    breakpoints = await session.set_breakpoints(str(source), [line])
    assert len(breakpoints) == 1 and breakpoints[0]["verified"]
    await session.request("next", {"threadId": thread_id})
    stepped = await event_named(session, "stopped")
    assert stepped["reason"] == "step"
    thread_id = stepped.get("threadId", thread_id)
    frames = (await session.request("stackTrace", {"threadId": thread_id}))["stackFrames"]
    assert frames[0]["line"] > line
    assert await session.set_breakpoints(str(source), []) == []
    await session.request("continue", {"threadId": thread_id})


async def exercise(config, source, line, finish, *, detached=None, finish_on_close=True):
    session = DebugSession(config)
    try:
        try:
            await asyncio.wait_for(session.start(), timeout=40)
        except DAPError as error:
            await asyncio.wait_for(session.close(), timeout=10)
            diagnostics = []
            async with asyncio.timeout(5):
                while True:
                    event = await session.next_event()
                    if event["event"] == "output":
                        diagnostics.append(event["body"].get("output", ""))
                    if event["event"] == "sessionState" and event["body"]["state"] == "closed":
                        break
            details = str(error) + "\n" + "".join(diagnostics)
            skip_debugging_restriction(details)
            error.add_note(details)
            raise
        async with asyncio.timeout(60):
            await inspect_and_step(session, source, line)
        if detached is None:
            finish.touch()
            await event_named(session, "terminated")
        else:
            await asyncio.wait_for(session.close(), timeout=10)
            process, log_path, heartbeat = detached
            before = heartbeat.read_text() if heartbeat.exists() else ""
            # A new heartbeat after disconnect proves the target is alive and running.
            wait_for_fixture(
                lambda: heartbeat.exists() and heartbeat.read_text() not in ["", before],
                process,
                log_path,
                timeout=10,
            )
    finally:
        if finish_on_close:
            finish.touch()
        await asyncio.wait_for(session.close(), timeout=10)


def test_python_launch(tmp_path, python_adapter):
    program, source, line = make_target(tmp_path, "python")
    heartbeat, finish = tmp_path / "heartbeat", tmp_path / "finish"
    config = config_for(
        tmp_path,
        "python",
        "launch",
        source,
        line,
        python=python_adapter,
        arguments={
            "program": str(program),
            "args": [str(heartbeat), str(finish)],
            "justMyCode": True,
        },
    )
    asyncio.run(exercise(config, source, line, finish))


@pytest.mark.parametrize("mapped", [False, True], ids=["same-path", "mapped-path"])
def test_python_tcp_attach_and_detach(tmp_path, python_adapter, mapped):
    program, source, line = make_target(tmp_path, "python", mapped=mapped)
    heartbeat, finish = tmp_path / "heartbeat", tmp_path / "finish"
    ready = tmp_path / "ready"
    launcher = tmp_path / "listen.py"
    launcher.write_text(PYTHON_LISTENER)
    port = available_port()
    config = config_for(
        tmp_path,
        "python",
        "attach",
        source,
        line,
        connect={"host": "127.0.0.1", "port": port},
        arguments={"justMyCode": True},
        pathMappings=[{"localRoot": str(source.parent), "remoteRoot": str(program.parent)}]
        if mapped
        else [],
    )
    argv = [
        python_adapter,
        str(launcher),
        str(port),
        str(ready),
        str(program),
        str(heartbeat),
        str(finish),
    ]
    with external_process(argv, tmp_path) as (process, log_path):
        wait_for_fixture(lambda: ready.exists() and heartbeat.exists(), process, log_path)
        asyncio.run(exercise(config, source, line, finish, detached=(process, log_path, heartbeat)))


def build_go(go, program, directory):
    binary = directory / "target"
    result = subprocess.run(
        [go, "build", "-gcflags=all=-N -l", "-o", str(binary), str(program)],
        cwd=directory,
        capture_output=True,
        text=True,
        timeout=90,
    )
    assert result.returncode == 0, f"Go fixture failed to build:\n{result.stdout}\n{result.stderr}"
    return binary


@pytest.mark.parametrize("mode", ["exec", "debug", "test"])
def test_go_launch(tmp_path, go_adapter, mode):
    go, dlv = go_adapter
    program, source, line = make_target(tmp_path, "go")
    target = build_go(go, program, tmp_path) if mode == "exec" else program.parent
    if mode == "test":
        (program.parent / "main_test.go").write_text(
            'package main\n\nimport "testing"\n\nfunc TestTarget(t *testing.T) { main() }\n'
        )
    heartbeat, finish = tmp_path / "heartbeat", tmp_path / "finish"
    config = config_for(
        tmp_path,
        "go",
        "launch",
        source,
        line,
        dlv=dlv,
        arguments={
            "mode": mode,
            "program": str(target),
            "cwd": str(program.parent),
            "args": [str(heartbeat), str(finish)],
        },
    )
    asyncio.run(exercise(config, source, line, finish))


@pytest.mark.parametrize("mapped", [False, True], ids=["same-path", "mapped-path"])
def test_go_headless_attach_and_detach(tmp_path, go_adapter, mapped):
    go, dlv = go_adapter
    program, source, line = make_target(tmp_path, "go", mapped=mapped)
    binary = build_go(go, program, tmp_path)
    heartbeat, finish = tmp_path / "heartbeat", tmp_path / "finish"
    port = available_port()
    config = config_for(
        tmp_path,
        "go",
        "attach",
        source,
        line,
        connect={"host": "127.0.0.1", "port": port},
        arguments={"mode": "remote"},
        pathMappings=[{"localRoot": str(source.parent), "remoteRoot": str(program.parent)}]
        if mapped
        else [],
    )
    argv = [
        dlv,
        "exec",
        str(binary),
        "--headless",
        f"--listen=127.0.0.1:{port}",
        "--accept-multiclient",
        "--continue",
        "--",
        str(heartbeat),
        str(finish),
    ]
    with external_process(argv, tmp_path) as (process, log_path):
        wait_for_fixture(heartbeat.exists, process, log_path)
        asyncio.run(
            exercise(
                config,
                source,
                line,
                finish,
                detached=(process, log_path, heartbeat),
                finish_on_close=False,
            )
        )
        asyncio.run(exercise(config, source, line, finish, detached=(process, log_path, heartbeat)))
