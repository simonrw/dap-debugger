"""Asyncio DAP framing and correlated requests."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Callable
from typing import Any

MAX_HEADER = 8 * 1024
MAX_BODY = 16 * 1024 * 1024


class DAPError(Exception):
    def __init__(self, command: str, message: str):
        self.command = command
        self.message = message
        super().__init__(f"{command}: {message}")


async def read_message(reader: asyncio.StreamReader) -> dict[str, Any]:
    try:
        header = bytearray()
        while not header.endswith(b"\r\n\r\n"):
            if len(header) >= MAX_HEADER:
                raise ValueError("DAP header exceeds 8 KiB")
            header.extend(await reader.readexactly(1))
        lengths = []
        for line in header[:-4].decode("ascii").split("\r\n"):
            key, separator, value = line.partition(":")
            if not separator:
                raise ValueError("malformed DAP header")
            if key.lower() == "content-length":
                if not value.strip().isascii() or not value.strip().isdigit():
                    raise ValueError("invalid Content-Length")
                lengths.append(int(value.strip()))
        if len(lengths) != 1 or not 0 < lengths[0] <= MAX_BODY:
            raise ValueError("Content-Length must occur once and be between 1 and 16 MiB")
        message = json.loads((await reader.readexactly(lengths[0])).decode("utf-8"))
        if not isinstance(message, dict):
            raise ValueError("DAP message must be an object")
        if type(message.get("seq")) is not int or message["seq"] < 0:
            raise ValueError("DAP message requires an integer seq")
        kind = message.get("type")
        if kind == "response":
            if type(message.get("request_seq")) is not int or message["request_seq"] < 0:
                raise ValueError("DAP response requires nonnegative request_seq")
            if type(message.get("success")) is not bool:
                raise ValueError("DAP response requires boolean success")
            if not isinstance(message.get("command"), str):
                raise ValueError("DAP response requires command")
        elif kind == "event":
            if not isinstance(message.get("event"), str):
                raise ValueError("DAP event requires event name")
        elif kind == "request":
            if not isinstance(message.get("command"), str):
                raise ValueError("DAP request requires command")
        else:
            raise ValueError("unknown DAP message type")
        if "body" in message and not isinstance(message["body"], dict):
            raise ValueError("DAP body must be an object")
        return message
    except asyncio.IncompleteReadError as error:
        raise DAPError("transport", "unexpected EOF from debug adapter") from error
    except (ValueError, UnicodeError, asyncio.LimitOverrunError) as error:
        raise DAPError("transport", f"invalid DAP frame: {error}") from error


def encode_message(message: dict[str, Any]) -> bytes:
    try:
        body = json.dumps(message, ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError, UnicodeError) as error:
        raise DAPError("transport", f"message is not valid UTF-8 JSON: {error}") from error
    if len(body) > MAX_BODY:
        raise DAPError("transport", "outgoing DAP message exceeds 16 MiB")
    return f"Content-Length: {len(body)}\r\n\r\n".encode("ascii") + body


class DAPConnection:
    def __init__(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
        *,
        on_event: Callable[[dict[str, Any]], None],
        on_error: Callable[[DAPError], None],
        request_timeout: float = 15,
    ):
        self.reader = reader
        self.writer = writer
        self.request_timeout = request_timeout
        self._on_event = on_event
        self._on_error = on_error
        self._sequence = 0
        self._pending: dict[int, tuple[str, asyncio.Future]] = {}
        self._write_lock = asyncio.Lock()
        self._failure: DAPError | None = None
        self._closed = False
        self._reader_task = asyncio.create_task(self._read(), name="dap-reader")

    async def _send(self, message: dict[str, Any]) -> None:
        async with self._write_lock:
            if self._closed or self._failure:
                raise DAPError("transport", "connection is closed")
            self.writer.write(encode_message(message))
            await self.writer.drain()

    def _next_sequence(self) -> int:
        self._sequence += 1
        return self._sequence

    async def request(
        self, command: str, arguments: dict[str, Any] | None = None, *, timeout: float | None = None
    ) -> dict[str, Any]:
        if self._closed or self._failure:
            raise DAPError(command, str(self._failure or "connection is closed"))
        sequence = self._next_sequence()
        future = asyncio.get_running_loop().create_future()
        self._pending[sequence] = (command, future)
        try:
            async with asyncio.timeout(self.request_timeout if timeout is None else timeout):
                await self._send(
                    {
                        "seq": sequence,
                        "type": "request",
                        "command": command,
                        "arguments": arguments or {},
                    }
                )
                return await future
        except TimeoutError as error:
            raise DAPError(command, "request timed out") from error
        except DAPError as error:
            if error.command != command:
                raise DAPError(command, error.message) from error
            raise
        except (OSError, ConnectionError) as error:
            self._fail(DAPError("transport", str(error)))
            raise DAPError(command, str(error)) from error
        finally:
            self._pending.pop(sequence, None)
            if not future.done():
                future.cancel()
            elif not future.cancelled():
                future.exception()

    def _fail(self, error: DAPError) -> None:
        if self._failure or self._closed:
            return
        self._failure = error
        for command, future in self._pending.values():
            if not future.done():
                future.set_exception(DAPError(command, error.message))
        self._on_error(error)

    async def _read(self) -> None:
        try:
            while True:
                message = await read_message(self.reader)
                kind = message["type"]
                if kind == "event":
                    message.setdefault("body", {})
                    self._on_event(message)
                elif kind == "response":
                    pending = self._pending.get(message["request_seq"])
                    if pending is None or pending[1].done():
                        continue
                    command, future = pending
                    if message["command"] != command:
                        raise DAPError("transport", "response command does not match its request")
                    if message["success"]:
                        future.set_result(message.get("body", {}))
                    else:
                        future.set_exception(
                            DAPError(
                                command, str(message.get("message", "adapter rejected request"))
                            )
                        )
                else:
                    async with asyncio.timeout(self.request_timeout):
                        await self._send(
                            {
                                "seq": self._next_sequence(),
                                "type": "response",
                                "request_seq": message["seq"],
                                "command": message["command"],
                                "success": False,
                                "message": "Client does not support reverse requests",
                            }
                        )
        except asyncio.CancelledError:
            raise
        except Exception as error:
            self._fail(error if isinstance(error, DAPError) else DAPError("transport", str(error)))

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        for command, future in self._pending.values():
            if not future.done():
                future.set_exception(DAPError(command, "connection closed"))
        self._reader_task.cancel()
        await asyncio.gather(self._reader_task, return_exceptions=True)
        self.writer.close()
        try:
            async with asyncio.timeout(1):
                await self.writer.wait_closed()
        except (TimeoutError, OSError):
            pass
