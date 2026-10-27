import asyncio
import json
from contextlib import asynccontextmanager

import pytest

from dap_debugger.protocol import DAPConnection, DAPError, encode_message, read_message

pytestmark = pytest.mark.asyncio


@asynccontextmanager
async def connected_pair(timeout=0.2):
    peers = asyncio.Queue()
    server = await asyncio.start_server(lambda r, w: peers.put_nowait((r, w)), "127.0.0.1", 0)
    reader, writer = await asyncio.open_connection("127.0.0.1", server.sockets[0].getsockname()[1])
    peer_reader, peer_writer = await peers.get()
    events, errors = [], []
    connection = DAPConnection(
        reader, writer, on_event=events.append, on_error=errors.append, request_timeout=timeout
    )
    try:
        yield connection, peer_reader, peer_writer, events, errors
    finally:
        await connection.close()
        peer_writer.close()
        await peer_writer.wait_closed()
        server.close()
        await server.wait_closed()


def response(request, *, body=None, success=True):
    return {
        "seq": request["seq"] + 100,
        "type": "response",
        "request_seq": request["seq"],
        "command": request["command"],
        "success": success,
        "body": body or {},
    }


async def test_utf8_fragmented_and_combined_frames():
    message = {"seq": 1, "type": "event", "event": "output", "body": {"output": "héllo 世界"}}
    frame = encode_message(message)
    header, body = frame.split(b"\r\n\r\n", 1)
    assert int(header.split(b":")[1]) == len(body)
    assert json.loads(body) == message
    reader = asyncio.StreamReader()
    pending = asyncio.create_task(read_message(reader))
    for fragment in [frame[:4], frame[4:23], frame[23:-3], frame[-3:] + frame]:
        reader.feed_data(fragment)
        await asyncio.sleep(0)
    assert await pending == message
    assert await read_message(reader) == message


async def test_concurrent_requests_correlate_reverse_responses_and_events():
    async with connected_pair() as (client, reader, writer, events, errors):
        first = asyncio.create_task(client.request("threads"))
        second = asyncio.create_task(client.request("stackTrace", {"threadId": 73}))
        requests = [await read_message(reader), await read_message(reader)]
        writer.write(encode_message({"seq": 90, "type": "event", "event": "initialized"}))
        for request in reversed(requests):
            writer.write(encode_message(response(request, body={"answer": request["command"]})))
        await writer.drain()
        assert await first == {"answer": "threads"}
        assert await second == {"answer": "stackTrace"}
        assert events == [{"seq": 90, "type": "event", "event": "initialized", "body": {}}]
        assert not errors


async def test_failed_response_timeout_cancellation_and_late_response():
    async with connected_pair(timeout=0.05) as (client, reader, writer, _, errors):
        failed = asyncio.create_task(client.request("evaluate"))
        request = await read_message(reader)
        writer.write(
            encode_message({**response(request, success=False), "message": "unknown name"})
        )
        with pytest.raises(DAPError, match="evaluate: unknown name"):
            await failed
        timed_out = asyncio.create_task(client.request("threads"))
        late = await read_message(reader)
        with pytest.raises(DAPError, match="timed out"):
            await timed_out
        cancelled = asyncio.create_task(client.request("scopes"))
        cancelled_request = await read_message(reader)
        cancelled.cancel()
        with pytest.raises(asyncio.CancelledError):
            await cancelled
        writer.write(encode_message(response(late)) + encode_message(response(cancelled_request)))
        healthy = asyncio.create_task(client.request("variables"))
        request = await read_message(reader)
        writer.write(encode_message(response(request, body={"variables": []})))
        assert await healthy == {"variables": []}
        assert not errors


@pytest.mark.parametrize(
    "frame",
    [
        b"Content-Length: 16777217\r\n\r\n",
        b"Content-Length: -1\r\n\r\n",
        b"Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}",
        b"Bad-Header\r\n\r\n",
        b"X: " + b"x" * 8192 + b"\r\nContent-Length: 2\r\n\r\n{}",
        b"Content-Length: 1\r\n\r\n{",
        encode_message({"seq": 1, "type": "response", "success": True, "command": "threads"}),
        encode_message({"seq": 1, "type": "event", "event": "stopped", "body": []}),
        b"",
    ],
)
async def test_bad_frames_fail_pending_requests_once(frame):
    async with connected_pair() as (client, reader, writer, _, errors):
        pending = [
            asyncio.create_task(client.request(command)) for command in ["threads", "scopes"]
        ]
        await read_message(reader)
        await read_message(reader)
        writer.write(frame)
        await writer.drain()
        writer.close()
        results = await asyncio.gather(*pending, return_exceptions=True)
        assert all(isinstance(result, DAPError) for result in results)
        assert len(errors) == 1


async def test_oversized_unterminated_header_fails_without_waiting_for_eof():
    async with connected_pair() as (client, reader, writer, _, errors):
        pending = asyncio.create_task(client.request("threads"))
        await read_message(reader)
        writer.write(b"X" * 8193)
        with pytest.raises(DAPError, match="header exceeds"):
            await asyncio.wait_for(pending, 0.5)
        assert len(errors) == 1


async def test_non_json_request_is_typed_error_and_connection_remains_usable():
    async with connected_pair() as (client, reader, writer, _, errors):
        with pytest.raises(DAPError, match="evaluate: message is not valid"):
            await client.request("evaluate", {"invalid": object()})
        pending = asyncio.create_task(client.request("threads"))
        request = await read_message(reader)
        writer.write(encode_message(response(request)))
        assert await pending == {}
        assert not errors


async def test_reverse_request_is_rejected_without_running_command():
    async with connected_pair() as (_, reader, writer, _, errors):
        writer.write(
            encode_message(
                {
                    "seq": 41,
                    "type": "request",
                    "command": "runInTerminal",
                    "arguments": {"args": ["must-not-run"]},
                }
            )
        )
        reply = await asyncio.wait_for(read_message(reader), 1)
        assert reply["request_seq"] == 41
        assert reply["success"] is False
        assert not errors
