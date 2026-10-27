"""Debugger lifecycle, adapter ownership, and breakpoint synchronization."""

from __future__ import annotations

import asyncio
import copy
import ipaddress
import socket
from collections import deque
from pathlib import Path
from typing import Any

from .config import SessionConfig, validate_lines
from .protocol import DAPConnection, DAPError

__all__ = ["DebugSession", "DAPError"]

_LIFECYCLE_COMMANDS = {"initialize", "launch", "attach", "configurationDone", "disconnect"}
_MAX_OUTPUT_EVENTS = 256
_MAX_OUTPUT_CHARS = 16 * 1024


class DebugSession:
    def __init__(self, config: SessionConfig):
        self.config = config
        self.state = "new"
        self.capabilities: dict[str, Any] = {}
        self._connection: DAPConnection | None = None
        self._process: asyncio.subprocess.Process | None = None
        self._diagnostics: set[asyncio.Task] = set()
        self._events: deque[dict[str, Any]] = deque()
        self._event_ready = asyncio.Event()
        self._output_count = 0
        self._initialized = asyncio.Event()
        self._transport_error: DAPError | None = None
        self._startup_task: asyncio.Task | None = None
        self._launch_task: asyncio.Task | None = None
        self._close_task: asyncio.Task | None = None
        self._termination_task: asyncio.Task | None = None
        self._cleanup_lock = asyncio.Lock()
        self._closing = False
        self._configured = False
        self._desired = copy.deepcopy(config.breakpoints)
        self._synced: dict[str, list[int]] = {}
        self._breakpoint_locks: dict[str, asyncio.Lock] = {}

    def _emit(self, event: dict[str, Any]) -> None:
        if self.state == "closed":
            return
        if event["event"] == "output":
            event = {**event, "body": dict(event.get("body", {}))}
            output = str(event["body"].get("output", ""))
            if len(output) > _MAX_OUTPUT_CHARS:
                output = output[:_MAX_OUTPUT_CHARS] + "\n[output truncated]\n"
            event["body"]["output"] = output
            if self._output_count >= _MAX_OUTPUT_EVENTS:
                for index, old in enumerate(self._events):
                    if old["event"] == "output":
                        del self._events[index]
                        self._output_count -= 1
                        break
            self._output_count += 1
        self._events.append(event)
        self._event_ready.set()

    def _state(self, state: str) -> None:
        if self.state == state or self.state == "closed":
            return
        # Queue the final event before closing the event stream to producers.
        if state == "closed":
            self._emit({"type": "event", "event": "sessionState", "body": {"state": state}})
            self.state = state
        else:
            self.state = state
            self._emit({"type": "event", "event": "sessionState", "body": {"state": state}})

    def _error(self, message: str) -> None:
        self._emit({"type": "event", "event": "sessionError", "body": {"message": message}})

    def _on_event(self, event: dict[str, Any]) -> None:
        name = event["event"]
        if name == "initialized":
            self._initialized.set()
        if not self._closing and self.state not in {"failed", "terminated", "closed"}:
            if name == "stopped":
                self._state("stopped")
            elif name == "continued":
                self._state("running")
            elif name == "terminated":
                self._state("terminated")
        if name == "capabilities":
            self.capabilities.update(event["body"].get("capabilities", {}))
        self._emit(event)
        if name == "terminated" and not self._closing and self._termination_task is None:
            self._termination_task = asyncio.create_task(
                self._finish_terminated(), name="dap-terminated"
            )

    async def _finish_terminated(self) -> None:
        # Launch can still be awaiting its response when terminated arrives.
        if self._startup_task:
            await asyncio.gather(self._startup_task, return_exceptions=True)
        await self._cleanup(terminate=self.config.request == "launch")

    def _on_transport_error(self, error: DAPError) -> None:
        self._transport_error = error
        self._initialized.set()
        if not self._closing and self.state not in {"terminated", "closed", "failed"}:
            self._state("failed")
            self._error(str(error))

    async def next_event(self) -> dict[str, Any]:
        while not self._events:
            if self.state == "closed":
                raise DAPError("events", "session is closed")
            self._event_ready.clear()
            await self._event_ready.wait()
        event = self._events.popleft()
        if event["event"] == "output":
            self._output_count -= 1
        return event

    async def start(self) -> None:
        if self.state != "new":
            raise DAPError("start", "a session can only be started once")
        self._state("starting")
        self._startup_task = asyncio.create_task(self._start(), name="dap-startup")
        try:
            async with asyncio.timeout(self.config.startup_timeout):
                await self._startup_task
        except asyncio.CancelledError:
            if not self._closing:
                await self.close()
            raise
        except Exception as error:
            failure = (
                error
                if isinstance(error, DAPError)
                else DAPError(
                    "start", "startup timed out" if isinstance(error, TimeoutError) else str(error)
                )
            )
            if self.state != "failed":
                self._state("failed")
                self._error(str(failure))
            await self._cleanup(terminate=self.config.request == "launch")
            raise failure from error

    async def _start(self) -> None:
        reader, writer = await self._open_transport()
        self._connection = DAPConnection(
            reader,
            writer,
            on_event=self._on_event,
            on_error=self._on_transport_error,
            request_timeout=self.config.request_timeout,
        )
        self.capabilities = await self._connection.request(
            "initialize",
            {
                "clientID": "dap-debugger",
                "clientName": "DAP Debugger",
                "adapterID": "debugpy" if self.config.adapter == "python" else "go",
                "pathFormat": "path",
                "linesStartAt1": True,
                "columnsStartAt1": True,
                "supportsVariableType": True,
                "supportsRunInTerminalRequest": False,
                "supportsStartDebuggingRequest": False,
            },
        )
        self._launch_task = asyncio.create_task(
            self._connection.request(
                self.config.request,
                copy.deepcopy(self.config.arguments),
                timeout=self.config.startup_timeout,
            ),
            name="dap-launch-or-attach",
        )
        initialized = asyncio.create_task(self._initialized.wait())
        try:
            done, _ = await asyncio.wait(
                [initialized, self._launch_task], return_when=asyncio.FIRST_COMPLETED
            )
            if self._launch_task in done:
                await self._launch_task
            await initialized
            if self._transport_error:
                raise self._transport_error
            while True:
                dirty = [
                    path for path, lines in self._desired.items() if self._synced.get(path) != lines
                ]
                if not dirty:
                    self._configured = True
                    break
                for path in dirty:
                    await self._sync_breakpoints(path)
            if "exceptionBreakpointFilters" in self.capabilities:
                await self._connection.request("setExceptionBreakpoints", {"filters": []})
            if self.capabilities.get("supportsConfigurationDoneRequest"):
                await self._connection.request("configurationDone")
            await self._launch_task
            if self._transport_error and self.state != "terminated":
                raise self._transport_error
            if self.state == "starting":
                self._state("running")
        finally:
            initialized.cancel()
            await asyncio.gather(initialized, return_exceptions=True)
            if not self._launch_task.done():
                self._launch_task.cancel()
            await asyncio.gather(self._launch_task, return_exceptions=True)

    async def request(
        self, command: str, arguments: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        if command in _LIFECYCLE_COMMANDS:
            raise DAPError(command, "command is managed by the session")
        if self._closing or self.state not in {"running", "stopped"} or not self._connection:
            raise DAPError(command, f"request unavailable while session is {self.state}")
        return await self._connection.request(command, arguments)

    async def set_breakpoints(self, path: str, lines: list[int]) -> list[dict[str, Any]]:
        if self._closing or self.state in {"failed", "terminated", "closed"}:
            raise DAPError("setBreakpoints", f"session is {self.state}")
        if not isinstance(path, str) or not Path(path).is_absolute():
            raise DAPError("setBreakpoints", "source path must be an absolute local path")
        try:
            lines = validate_lines(lines)
        except ValueError as error:
            raise DAPError("setBreakpoints", str(error)) from error
        self._desired[path] = lines
        if not self._configured:
            return [{"verified": False, "line": line, "source": {"path": path}} for line in lines]
        return await self._sync_breakpoints(path)

    async def _sync_breakpoints(self, path: str) -> list[dict[str, Any]]:
        lock = self._breakpoint_locks.setdefault(path, asyncio.Lock())
        async with lock:
            lines = self._desired[path].copy()
            if self._connection is None:
                raise DAPError("setBreakpoints", "adapter is not connected")
            body = await self._connection.request(
                "setBreakpoints",
                {
                    "source": {"path": path},
                    "breakpoints": [{"line": line} for line in lines],
                    "sourceModified": False,
                },
            )
            records = body.get("breakpoints", [])
            if not isinstance(records, list) or any(not isinstance(item, dict) for item in records):
                raise DAPError("setBreakpoints", "adapter returned invalid breakpoint records")
            self._synced[path] = lines
            return records

    def _drain_diagnostics(self, stream: asyncio.StreamReader | None, category: str) -> None:
        if stream is None:
            return
        task = asyncio.create_task(self._read_diagnostics(stream, category))
        self._diagnostics.add(task)
        task.add_done_callback(self._diagnostics.discard)

    async def _read_diagnostics(self, stream: asyncio.StreamReader, category: str) -> None:
        try:
            while chunk := await stream.read(4096):
                self._emit(
                    {
                        "type": "event",
                        "event": "output",
                        "body": {
                            "category": category,
                            "output": chunk.decode("utf-8", errors="replace"),
                        },
                    }
                )
        except (OSError, ValueError) as error:
            self._error(f"Cannot read adapter diagnostics: {error}")

    async def _open_transport(self) -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
        if self.config.request == "attach":
            endpoint = self.config.connect
            if endpoint is None:
                raise DAPError("attach", "connect endpoint is required")
            host = endpoint["host"]
            try:
                loopback = ipaddress.ip_address(host).is_loopback
            except ValueError:
                loopback = host.lower() == "localhost"
            if not loopback:
                self._emit(
                    {
                        "type": "event",
                        "event": "output",
                        "body": {
                            "category": "console",
                            "output": "Warning: DAP is unauthenticated and unencrypted. Use an SSH tunnel for remote endpoints.\n",
                        },
                    }
                )
            return await asyncio.open_connection(host, endpoint["port"])
        if self.config.adapter == "python":
            # debugpy otherwise gives the debuggee control of the TUI's terminal.
            self._process = await asyncio.create_subprocess_exec(
                self.config.python,
                "-m",
                "debugpy.adapter",
                start_new_session=True,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            self._drain_diagnostics(self._process.stderr, "console")
            assert self._process.stdout is not None and self._process.stdin is not None
            return self._process.stdout, self._process.stdin
        last_error = "Delve did not become ready"
        for _ in range(3):
            with socket.socket() as reservation:
                reservation.bind(("127.0.0.1", 0))
                port = reservation.getsockname()[1]
            self._process = await asyncio.create_subprocess_exec(
                self.config.dlv,
                "dap",
                f"--listen=127.0.0.1:{port}",
                cwd=self.config.arguments.get("cwd"),
                start_new_session=True,
                stdin=asyncio.subprocess.DEVNULL,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            self._drain_diagnostics(self._process.stdout, "console")
            self._drain_diagnostics(self._process.stderr, "console")
            deadline = asyncio.get_running_loop().time() + min(10, self.config.startup_timeout)
            while asyncio.get_running_loop().time() < deadline:
                if self._process.returncode is not None:
                    last_error = f"Delve exited before accepting connections (status {self._process.returncode})"
                    break
                try:
                    return await asyncio.wait_for(asyncio.open_connection("127.0.0.1", port), 0.25)
                except (OSError, TimeoutError) as error:
                    last_error = str(error)
                    await asyncio.sleep(0.05)
            await self._reap_process()
        raise DAPError("launch", f"Cannot connect to local Delve: {last_error}")

    async def close(self, *, terminate: bool | None = None) -> None:
        if self._close_task is None:
            self._closing = True
            self._close_task = asyncio.create_task(self._close(terminate), name="dap-close")
        await asyncio.shield(self._close_task)

    async def _close(self, terminate: bool | None) -> None:
        if terminate is None:
            terminate = self.config.request == "launch"
        can_terminate = self.capabilities.get("supportTerminateDebuggee") or (
            self.config.adapter == "python" and self.capabilities.get("supportsTerminateDebuggee")
        )
        if terminate and self.config.request == "attach" and not can_terminate:
            self._error(
                "Adapter does not support explicit termination of an attached target; disconnecting only"
            )
            terminate = False
        if self._startup_task and not self._startup_task.done():
            self._startup_task.cancel()
            await asyncio.gather(self._startup_task, return_exceptions=True)
        try:
            if self._termination_task:
                await self._termination_task
            await self._cleanup(terminate=terminate)
        finally:
            self._state("closed")

    async def _cleanup(self, *, terminate: bool) -> None:
        async with self._cleanup_lock:
            if self._connection:
                connection, self._connection = self._connection, None
                try:
                    await connection.request(
                        "disconnect",
                        {"terminateDebuggee": terminate},
                        timeout=min(2, self.config.request_timeout),
                    )
                except Exception as error:
                    if not self._transport_error and self.state != "terminated":
                        self._error(f"Disconnect failed: {error}")
                finally:
                    await connection.close()
            await self._reap_process()
            tasks = list(self._diagnostics)
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)

    async def _reap_process(self) -> None:
        process, self._process = self._process, None
        if process is None or process.returncode is not None:
            return
        try:
            process.terminate()
            try:
                await asyncio.wait_for(process.wait(), 1)
            except TimeoutError:
                process.kill()
                await asyncio.wait_for(process.wait(), 1)
        except ProcessLookupError:
            pass
        except (OSError, TimeoutError) as error:
            self._error(f"Could not reap owned adapter: {error}")
