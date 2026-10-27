import asyncio
import json
import os
import sys
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import pytest

from dap_debugger.config import SessionConfig
from dap_debugger.protocol import encode_message, read_message
from dap_debugger.session import DAPError, DebugSession

pytestmark = pytest.mark.asyncio


class FakeAdapter:
    def __init__(self):
        self.requests = []
        self.writers = []
        self.tasks = []
        self.connected = asyncio.Event()
        self.attach_received = asyncio.Event()
        self.disconnect_received = asyncio.Event()
        self.connection_closed = asyncio.Event()
        self.allow_initialized = asyncio.Event()
        self.allow_initialized.set()
        self.breakpoint_received = asyncio.Event()
        self.allow_breakpoint = asyncio.Event()
        self.allow_breakpoint.set()
        self.capabilities: dict[str, Any] = {"supportsConfigurationDoneRequest": True}
        self.fail_attach = False
        self.stop_early = True
        self.ignore_disconnect = False
        self.attach_request = None
        self.sequence = 0

    def accept(self, reader, writer):
        self.writers.append(writer)
        self.connected.set()
        self.tasks.append(asyncio.create_task(self.serve(reader, writer)))

    def send(self, writer, message):
        self.sequence += 1
        writer.write(encode_message({"seq": self.sequence, **message}))

    def respond(self, writer, request, body=None, success=True):
        self.send(
            writer,
            {
                "type": "response",
                "request_seq": request["seq"],
                "command": request["command"],
                "success": success,
                "body": body or {},
            },
        )

    def event(self, name, body=None):
        self.send(self.writers[-1], {"type": "event", "event": name, "body": body or {}})

    async def serve(self, reader, writer):
        try:
            while True:
                request = await read_message(reader)
                self.requests.append(request)
                command = request["command"]
                if command == "initialize":
                    self.respond(writer, request, self.capabilities)
                elif command == "attach":
                    self.attach_request = request
                    self.attach_received.set()
                    if self.fail_attach:
                        self.respond(writer, request, success=False)
                        continue
                    await self.allow_initialized.wait()
                    self.event("initialized")
                    if not self.capabilities.get("supportsConfigurationDoneRequest"):
                        self.respond(writer, request)
                elif command == "setBreakpoints":
                    self.breakpoint_received.set()
                    await self.allow_breakpoint.wait()
                    self.respond(
                        writer,
                        request,
                        {
                            "breakpoints": [
                                {"verified": True, "line": bp["line"] + 1}
                                for bp in request["arguments"]["breakpoints"]
                            ]
                        },
                    )
                elif command == "configurationDone":
                    if self.stop_early:
                        self.event("stopped", {"threadId": 73, "reason": "breakpoint"})
                    self.respond(writer, request)
                    self.respond(writer, self.attach_request)
                elif command == "disconnect":
                    self.disconnect_received.set()
                    if not self.ignore_disconnect:
                        self.respond(writer, request)
                else:
                    self.respond(writer, request)
                await writer.drain()
        except (DAPError, ConnectionError):
            pass
        finally:
            self.connection_closed.set()


@asynccontextmanager
async def fake_session(tmp_path, adapter=None, **settings):
    adapter = adapter or FakeAdapter()
    server = await asyncio.start_server(adapter.accept, "127.0.0.1", 0)
    config = SessionConfig.from_dict(
        {
            "adapter": "go",
            "request": "attach",
            "connect": {"host": "127.0.0.1", "port": server.sockets[0].getsockname()[1]},
            "requestTimeout": 0.3,
            "startupTimeout": 2,
            **settings,
        },
        base_dir=tmp_path,
    )
    session = DebugSession(config)
    try:
        yield session, adapter
    finally:
        await session.close()
        for writer in adapter.writers:
            writer.close()
            await writer.wait_closed()
        for task in adapter.tasks:
            task.cancel()
        await asyncio.gather(*adapter.tasks, return_exceptions=True)
        server.close()
        await server.wait_closed()


async def collect_until_closed(session):
    events = []
    async with asyncio.timeout(2):
        while True:
            event = await session.next_event()
            events.append(event)
            if event["event"] == "sessionState" and event["body"]["state"] == "closed":
                return events


async def test_handshake_delayed_attach_response_early_stop_and_detach(tmp_path):
    async with fake_session(tmp_path, breakpoints={"app.go": [4]}) as (session, adapter):
        await session.start()
        assert session.state == "stopped"
        assert await session.request("threads") == {}
        await asyncio.gather(session.close(), session.close())
        commands = [request["command"] for request in adapter.requests]
        assert commands == [
            "initialize",
            "attach",
            "setBreakpoints",
            "configurationDone",
            "threads",
            "disconnect",
        ]
        assert adapter.requests[1]["arguments"]["mode"] == "remote"
        assert adapter.requests[-1]["arguments"] == {"terminateDebuggee": False}
        assert adapter.requests[0]["arguments"]["supportsRunInTerminalRequest"] is False
        events = await collect_until_closed(session)
        assert any(
            event["event"] == "stopped" and event["body"]["threadId"] == 73 for event in events
        )
        assert events[-1]["body"] == {"state": "closed"}
        with pytest.raises(DAPError, match="only be started once"):
            await session.start()


async def test_breakpoint_updates_before_and_during_startup_and_rapid_replacement(tmp_path):
    adapter = FakeAdapter()
    adapter.allow_breakpoint.clear()
    path = str(tmp_path / "app.go")
    async with fake_session(tmp_path, adapter) as (session, _):
        pending = await session.set_breakpoints(path, [2])
        assert pending[0]["verified"] is False
        starting = asyncio.create_task(session.start())
        await asyncio.wait_for(adapter.breakpoint_received.wait(), 1)
        await session.set_breakpoints(path, [8])
        adapter.allow_breakpoint.set()
        await starting
        breakpoint_requests = [
            request for request in adapter.requests if request["command"] == "setBreakpoints"
        ]
        assert [request["arguments"]["breakpoints"] for request in breakpoint_requests] == [
            [{"line": 2}],
            [{"line": 8}],
        ]
        assert await session.set_breakpoints(path, [10]) == [{"verified": True, "line": 11}]
        await asyncio.gather(
            session.set_breakpoints(path, [12]),
            session.set_breakpoints(path, [14]),
            session.set_breakpoints(path, []),
        )
        breakpoint_requests = [
            request for request in adapter.requests if request["command"] == "setBreakpoints"
        ]
        assert breakpoint_requests[-1]["arguments"]["breakpoints"] == []
        assert all(
            request["arguments"]["source"]["path"] == path for request in breakpoint_requests
        )


async def test_optional_configuration_capabilities(tmp_path):
    adapter = FakeAdapter()
    adapter.capabilities = {"exceptionBreakpointFilters": []}
    async with fake_session(tmp_path, adapter) as (session, _):
        await session.start()
        assert session.state == "running"
        commands = [request["command"] for request in adapter.requests]
        assert commands == ["initialize", "attach", "setExceptionBreakpoints"]
        with pytest.raises(DAPError, match="managed by the session"):
            await session.request("disconnect")


@pytest.mark.parametrize("failure", ["reject", "timeout", "cancel"])
async def test_startup_failure_and_cancellation_cleanup(tmp_path, failure):
    adapter = FakeAdapter()
    adapter.fail_attach = failure == "reject"
    if failure != "reject":
        adapter.allow_initialized.clear()
    async with fake_session(tmp_path, adapter, startupTimeout=0.1, requestTimeout=0.05) as (
        session,
        _,
    ):
        starting = asyncio.create_task(session.start())
        await asyncio.wait_for(adapter.attach_received.wait(), 1)
        if failure == "cancel":
            starting.cancel()
            with pytest.raises(asyncio.CancelledError):
                await starting
        else:
            with pytest.raises(DAPError):
                await starting
            assert session.state == "failed"
        await asyncio.wait_for(session.close(), 0.5)
        assert session.state == "closed"


async def test_close_during_startup_and_unresponsive_disconnect_are_bounded(tmp_path):
    adapter = FakeAdapter()
    adapter.allow_initialized.clear()
    async with fake_session(tmp_path, adapter, requestTimeout=0.05) as (session, _):
        starting = asyncio.create_task(session.start())
        await asyncio.wait_for(adapter.attach_received.wait(), 1)
        await asyncio.wait_for(session.close(), 0.5)
        with pytest.raises(asyncio.CancelledError):
            await starting
        assert session.state == "closed"


async def test_natural_termination_disconnects_once_without_closing_event_stream(tmp_path):
    async with fake_session(tmp_path) as (session, adapter):
        await session.start()
        adapter.event("exited", {"exitCode": 0})
        adapter.event("terminated")
        await asyncio.wait_for(adapter.disconnect_received.wait(), 1)
        await asyncio.wait_for(adapter.connection_closed.wait(), 1)
        assert session.state == "terminated"
        with pytest.raises(DAPError, match="terminated"):
            await session.request("continue", {"threadId": 73})
        await session.close()
        events = await collect_until_closed(session)
        assert sum(request["command"] == "disconnect" for request in adapter.requests) == 1
        assert any(event["event"] == "terminated" for event in events)
        assert not any(event["event"] == "sessionError" for event in events)


async def test_close_before_start_and_cancelled_close_still_cleanup(tmp_path):
    async with fake_session(tmp_path) as (session, adapter):
        await session.close()
        assert not adapter.requests
        assert session.state == "closed"
    adapter = FakeAdapter()
    adapter.ignore_disconnect = True
    async with fake_session(tmp_path, adapter, requestTimeout=0.05) as (session, _):
        await session.start()
        closing = asyncio.create_task(session.close())
        await asyncio.wait_for(adapter.disconnect_received.wait(), 1)
        closing.cancel()
        with pytest.raises(asyncio.CancelledError):
            await closing
        events = await collect_until_closed(session)
        assert session.state == "closed"
        assert sum(request["command"] == "disconnect" for request in adapter.requests) == 1
        assert any(event["event"] == "sessionError" for event in events)


async def test_startup_uses_its_own_deadline_not_ordinary_request_timeout(tmp_path):
    adapter = FakeAdapter()
    adapter.allow_initialized.clear()
    async with fake_session(tmp_path, adapter, requestTimeout=0.03, startupTimeout=0.5) as (
        session,
        _,
    ):
        starting = asyncio.create_task(session.start())
        await asyncio.wait_for(adapter.attach_received.wait(), 1)
        release = asyncio.get_running_loop().call_later(0.09, adapter.allow_initialized.set)
        try:
            await starting
            assert session.state == "stopped"
        finally:
            release.cancel()


async def test_output_flood_retains_lifecycle_and_does_not_block_requests(tmp_path):
    async with fake_session(tmp_path) as (session, adapter):
        await session.start()
        for number in range(1000):
            adapter.event("output", {"category": "stdout", "output": f"line {number}\n"})
        adapter.event("continued", {"threadId": 73})
        assert await session.request("threads") == {}
        assert session.state == "running"
        await session.close()
        events = await collect_until_closed(session)
        outputs = [event for event in events if event["event"] == "output"]
        assert len(outputs) <= 256
        assert outputs[-1]["body"]["output"] == "line 999\n"
        assert any(event["event"] == "stopped" for event in events)
        assert any(event["event"] == "continued" for event in events)


async def test_transport_failure_is_visible_once_and_invalid_lifecycle_rejected(tmp_path):
    async with fake_session(tmp_path) as (session, adapter):
        with pytest.raises(DAPError, match="new"):
            await session.request("threads")
        await session.start()
        adapter.writers[-1].write(b"Content-Length: nope\r\n\r\n")
        async with asyncio.timeout(1):
            while session.state != "failed":
                await session.next_event()
        await session.close()
        events = await collect_until_closed(session)
        assert len([event for event in events if event["event"] == "sessionError"]) == 1


@pytest.mark.parametrize("supported", [False, True])
async def test_attach_termination_requires_adapter_support(tmp_path, supported):
    adapter = FakeAdapter()
    adapter.capabilities["supportTerminateDebuggee"] = supported
    async with fake_session(tmp_path, adapter) as (session, _):
        await session.start()
        await session.close(terminate=True)
        assert adapter.requests[-1]["arguments"]["terminateDebuggee"] is supported


# The executable accepts the same argv as Python, but implements a deterministic stdio adapter.
_STDIO_ADAPTER = """
import json, os, sys
log = open(sys.argv[0] + ".log", "w")
log.write(json.dumps({"pid": os.getpid(), "argv": sys.argv[1:], "cwd": os.getcwd()}) + "\\n")
log.flush()
sequence = 0
launch = None
if sys.argv[1] == "dap":
    import socket
    host, port = sys.argv[2].split("=", 1)[1].split(":")
    listener = socket.socket()
    listener.bind((host, int(port)))
    listener.listen(1)
    connection, _ = listener.accept()
    sys.stdin = connection.makefile("r")
    sys.stdout = connection.makefile("w")
def send(message):
    global sequence
    sequence += 1
    body = json.dumps({"seq": sequence, **message}).encode()
    sys.stdout.buffer.write(f"Content-Length: {len(body)}\\r\\n\\r\\n".encode() + body)
    sys.stdout.buffer.flush()
def response(request):
    send({"type": "response", "request_seq": request["seq"], "command": request["command"],
          "success": True, "body": {"supportsConfigurationDoneRequest": True} if request["command"] == "initialize" else {}})
while True:
    header = sys.stdin.buffer.readline()
    if not header:
        break
    length = int(header.split(b":")[1])
    sys.stdin.buffer.readline()
    request = json.loads(sys.stdin.buffer.read(length))
    log.write(json.dumps(request) + "\\n")
    log.flush()
    if request["command"] == "launch":
        launch = request
        send({"type": "event", "event": "initialized"})
    else:
        response(request)
        if request["command"] == "configurationDone":
            response(launch)
"""


@pytest.mark.skipif(os.name == "nt", reason="fixture uses a POSIX executable script")
@pytest.mark.parametrize("adapter", ["python", "go"])
async def test_owned_launch_argv_build_cwd_and_termination(tmp_path, adapter):
    executable = tmp_path / "fake-python"
    executable.write_text(f"#!{sys.executable}\n" + _STDIO_ADAPTER)
    executable.chmod(0o755)
    build_dir = tmp_path / "module"
    build_dir.mkdir()
    config = SessionConfig.from_dict(
        {
            "adapter": adapter,
            "request": "launch",
            "python": str(executable),
            "dlv": str(executable),
            "arguments": {"program": ".", "cwd": "module"},
        },
        base_dir=tmp_path,
    )
    session = DebugSession(config)
    try:
        await session.start()
        assert session.state == "running"
    finally:
        await session.close()
    records = [json.loads(line) for line in Path(str(executable) + ".log").read_text().splitlines()]
    if adapter == "python":
        assert records[0]["argv"] == ["-m", "debugpy.adapter"]
    else:
        assert records[0]["argv"][0] == "dap"
        assert records[0]["argv"][1].startswith("--listen=127.0.0.1:")
        assert records[0]["cwd"] == str(build_dir)
    assert records[-1]["command"] == "disconnect"
    assert records[-1]["arguments"]["terminateDebuggee"] is True
    with pytest.raises(ProcessLookupError):
        os.kill(records[0]["pid"], 0)


async def test_missing_owned_adapter_is_actionable_and_close_is_safe(tmp_path):
    config = SessionConfig.from_dict(
        {
            "adapter": "python",
            "request": "launch",
            "python": str(tmp_path / "missing-python"),
            "arguments": {"program": "app.py"},
        },
        base_dir=tmp_path,
    )
    session = DebugSession(config)
    with pytest.raises(DAPError, match="missing-python"):
        await session.start()
    await session.close()
    assert session.state == "closed"
