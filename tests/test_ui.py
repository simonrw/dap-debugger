"""Headless UI tests through the public session seam and user interactions."""

from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import pytest
from rich.text import Text
from textual.widgets import Button, DataTable, Input, RichLog, Static, TabbedContent, Tabs, Tree

from dap_debugger.config import SessionConfig
from dap_debugger.ui import DebuggerApp, HelpScreen


class FakeSession:
    def __init__(self, config: SessionConfig):
        self.config = config
        self.state = "new"
        self.capabilities: dict[str, Any] = {}
        self.events: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.start_gate = asyncio.Event()
        self.start_gate.set()
        self.close_gate = asyncio.Event()
        self.close_gate.set()
        self.blocked: dict[str, asyncio.Event] = {}
        self.failures: dict[str, str] = {}
        self.close_calls = 0
        self.breakpoint_calls: list[tuple[str, list[int]]] = []
        self.breakpoint_response: list[dict[str, Any]] | None = None
        self.source: dict[str, Any] = {"path": config.arguments["program"]}

    async def start(self) -> None:
        self.state = "starting"
        await self.start_gate.wait()
        if "start" in self.failures:
            raise RuntimeError(self.failures["start"])
        if self.state == "starting":
            self.state = "running"

    async def next_event(self) -> dict[str, Any]:
        return await self.events.get()

    def emit(self, event: str, **body: Any) -> None:
        if event == "stopped":
            self.state = "stopped"
        elif event == "continued":
            self.state = "running"
        elif event in ["terminated", "sessionState"]:
            self.state = body.get("state", "terminated")
        self.events.put_nowait({"type": "event", "event": event, "body": body})

    async def request(
        self, command: str, arguments: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        arguments = arguments or {}
        self.calls.append((command, arguments))
        gate = self.blocked.get(command)
        if gate:
            await gate.wait()
        if command in self.failures:
            raise RuntimeError(self.failures[command])
        if command == "threads":
            return {
                "threads": [{"id": 17, "name": "[bold]main[/bold]"}, {"id": 29, "name": "worker"}]
            }
        if command == "stackTrace":
            ids = [101, 202] if arguments["threadId"] == 17 else [303]
            return {
                "stackFrames": [
                    {
                        "id": frame_id,
                        "name": f"frame {frame_id}",
                        "line": index + 2,
                        "source": dict(self.source),
                    }
                    for index, frame_id in enumerate(ids)
                ]
            }
        if command == "scopes":
            return {"scopes": [{"name": "Locals", "variablesReference": arguments["frameId"] * 10}]}
        if command == "variables":
            if arguments["variablesReference"] == 9000:
                return {
                    "variables": [
                        {
                            "name": "child",
                            "value": "[bold]literal[/bold]\x1b[31m red",
                            "variablesReference": 0,
                        }
                    ]
                }
            return {"variables": [{"name": "object", "value": "{...}", "variablesReference": 9000}]}
        if command == "evaluate":
            return {"result": "[bold]42[/bold]\x1b[31m!", "variablesReference": 8000}
        if command == "source":
            return {"content": "adapter line one\nadapter line two\n"}
        return {}

    async def set_breakpoints(self, path: str, lines: list[int]) -> list[dict[str, Any]]:
        self.breakpoint_calls.append((path, list(lines)))
        gate = self.blocked.get("setBreakpoints")
        if gate:
            await gate.wait()
        if self.breakpoint_response is not None:
            return self.breakpoint_response
        return [{"id": line, "line": line, "verified": True} for line in lines]

    async def close(self, *, terminate: bool | None = None) -> None:
        self.close_calls += 1
        await self.close_gate.wait()
        self.emit("sessionState", state="closed")


@pytest.fixture
def session(tmp_path: Path) -> FakeSession:
    source = tmp_path / "target.py"
    source.write_text("first = 1\nvalue = '[bold]literal[/bold]'\nprint(value)\n", encoding="utf-8")
    config = SessionConfig.from_dict(
        {
            "name": "[bold]Test[/bold]",
            "adapter": "python",
            "request": "launch",
            "arguments": {"program": str(source)},
        },
        base_dir=tmp_path,
    )
    return FakeSession(config)


async def until(pilot: Any, predicate: Any) -> None:
    async with asyncio.timeout(5):
        while not predicate():
            await pilot.pause(0.01)
        await pilot.pause()


def calls(session: FakeSession, command: str) -> list[dict[str, Any]]:
    return [arguments for name, arguments in session.calls if name == command]


def log_text(app: DebuggerApp, name: str) -> str:
    return "\n".join(line.text for line in app.query_one(f"#{name}", RichLog).lines)


async def stopped(app: DebuggerApp, pilot: Any, session: FakeSession) -> None:
    await until(pilot, lambda: bool(calls(session, "threads")))
    session.emit("stopped", threadId=17, reason="breakpoint")
    await until(pilot, lambda: app.query_one("#source", DataTable).row_count == 3)
    await until(pilot, lambda: bool(app.query_one("#variables", Tree).root.children))


async def test_start_inspect_expand_evaluate_and_literal_text(session: FakeSession) -> None:
    session.start_gate.clear()
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await pilot.press("f1")
        assert isinstance(app.screen, HelpScreen)
        await pilot.press("escape", "ctrl+e", "x")
        assert app.query_one("#expression", Input).value == "x"
        assert not calls(session, "evaluate")
        session.start_gate.set()
        await stopped(app, pilot, session)
        assert calls(session, "stackTrace")[-1] == {"threadId": 17, "startFrame": 0, "levels": 100}
        assert calls(session, "scopes")[-1] == {"frameId": 101}
        assert not calls(session, "variables")
        assert (
            app.query_one("#source", DataTable).get_row("2")[2].plain
            == "value = '[bold]literal[/bold]'"
        )
        assert app.query_one("#threads", DataTable).get_row("17")[1].plain == "[bold]main[/bold]"

        app.query_one("#inspectors", TabbedContent).active = "variables-tab"
        await pilot.pause()
        tree = app.query_one("#variables", Tree)
        tree.focus()
        await pilot.press("down", "enter")
        await until(pilot, lambda: bool(calls(session, "variables")))
        assert calls(session, "variables")[-1] == {"variablesReference": 1010}
        await pilot.press("down", "enter")
        await until(pilot, lambda: len(calls(session, "variables")) == 2)
        leaf = tree.root.children[0].children[0].children[0]
        label = leaf.label
        assert isinstance(label, Text)
        assert label.plain == "child = [bold]literal[/bold] red"
        assert not label.spans

        await pilot.press("ctrl+e", "enter")
        await until(pilot, lambda: "[bold]42[/bold]!" in log_text(app, "results"))
        assert calls(session, "evaluate")[-1] == {
            "expression": "x",
            "frameId": 101,
            "context": "repl",
        }
        session.emit("output", output="[red]target[/red]\x1b[2J\x00\n", category="stdout")
        app.query_one("#logs", TabbedContent).active = "output-tab"
        await until(pilot, lambda: "[red]target[/red]" in log_text(app, "output"))
        assert "42" not in log_text(app, "output")
        assert "\x1b" not in log_text(app, "output")
    assert session.close_calls == 1


async def test_select_thread_frame_and_discard_old_frame_results(session: FakeSession) -> None:
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await stopped(app, pilot, session)
        old_scopes = asyncio.Event()
        session.blocked["scopes"] = old_scopes
        stack = app.query_one("#stack", DataTable)
        stack.focus()
        stack.move_cursor(row=1)
        await pilot.press("enter")
        await until(pilot, lambda: calls(session, "scopes")[-1] == {"frameId": 202})
        del session.blocked["scopes"]
        threads = app.query_one("#threads", DataTable)
        threads.focus()
        threads.move_cursor(row=1)
        await pilot.press("enter")
        await until(pilot, lambda: calls(session, "scopes")[-1] == {"frameId": 303})
        old_scopes.set()
        await pilot.pause()
        await pilot.press("ctrl+e", "v", "enter")
        await until(pilot, lambda: bool(calls(session, "evaluate")))
        assert calls(session, "stackTrace")[-1]["threadId"] == 29
        assert calls(session, "evaluate")[-1]["frameId"] == 303
        data = app.query_one("#variables", Tree).root.children[0].data
        assert data is not None
        assert data["reference"] == 3030


@pytest.mark.parametrize("delayed", ["stackTrace", "source", "variables"])
async def test_resume_discards_delayed_inspection(session: FakeSession, delayed: str) -> None:
    gate = asyncio.Event()
    session.blocked[delayed] = gate
    if delayed == "source":
        session.source = {"name": "generated", "sourceReference": 77}
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await until(pilot, lambda: bool(calls(session, "threads")))
        session.emit("stopped", threadId=29)
        if delayed == "variables":
            await until(pilot, lambda: bool(app.query_one("#variables", Tree).root.children))
            app.query_one("#inspectors", TabbedContent).active = "variables-tab"
            await pilot.pause()
            app.query_one("#variables", Tree).focus()
            await pilot.press("down", "enter")
        await until(pilot, lambda: bool(calls(session, delayed)))
        await pilot.press("f5")
        await until(pilot, lambda: bool(calls(session, "continue")))
        gate.set()
        await pilot.pause()
        assert calls(session, "continue")[-1] == {"threadId": 29}
        assert app.query_one("#stack", DataTable).row_count == 0
        assert not app.query_one("#variables", Tree).root.children
        assert app.query_one("#evaluate", Button).disabled
        if delayed == "source":
            assert app.query_one("#source", DataTable).row_count == 0


@pytest.mark.parametrize(
    "key,button,command",
    [
        ("f5", "continue", "continue"),
        ("f10", "step-over", "next"),
        ("f11", "step-into", "stepIn"),
        ("shift+f11", "step-out", "stepOut"),
    ],
)
async def test_execution_buttons_and_keys(
    session: FakeSession, key: str, button: str, command: str
) -> None:
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await stopped(app, pilot, session)
        await pilot.press(key)
        await until(pilot, lambda: len(calls(session, command)) == 1)
        assert app.query_one("#continue", Button).disabled
        await pilot.press("f6")
        await until(pilot, lambda: len(calls(session, "pause")) == 1)
        session.emit("stopped", threadId=29)
        await until(pilot, lambda: not app.query_one(f"#{button}", Button).disabled)
        await pilot.click(f"#{button}")
        await until(pilot, lambda: len(calls(session, command)) == 2)
        assert calls(session, command) == [{"threadId": 17}, {"threadId": 29}]
        assert calls(session, "pause") == [{"threadId": 17}]


async def test_breakpoint_pending_relocated_rejected_and_rapid_removal(
    session: FakeSession,
) -> None:
    gate = asyncio.Event()
    session.blocked["setBreakpoints"] = gate
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await stopped(app, pilot, session)
        source = app.query_one("#source", DataTable)
        source.focus()
        await pilot.press("f9")
        await until(pilot, lambda: len(session.breakpoint_calls) == 1)
        assert source.get_row("2")[0].plain == ">?"
        session.breakpoint_response = [{"id": 4, "verified": True, "line": 3}]
        gate.set()
        await until(pilot, lambda: source.get_row("2")[0].plain == ">R")
        assert source.get_row("3")[0].plain == " B"
        session.emit(
            "breakpoint",
            reason="changed",
            breakpoint={"id": 4, "verified": False, "message": "not executable"},
        )
        await until(pilot, lambda: source.get_row("2")[0].plain == ">!")
        gate.clear()
        await pilot.click("#breakpoint")
        await until(pilot, lambda: len(session.breakpoint_calls) == 2)
        await pilot.press("f9", "f9")
        session.breakpoint_response = None
        gate.set()
        await until(pilot, lambda: len(session.breakpoint_calls) == 3)
        assert session.breakpoint_calls[-1] == (session.config.arguments["program"], [])
        assert source.get_row("2")[0].plain == "> "


async def test_missing_source_fallback_failure_resize_and_cleanup(session: FakeSession) -> None:
    session.source = {"path": "/missing/source.py", "sourceReference": 9}
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await until(pilot, lambda: bool(calls(session, "threads")))
        session.emit("stopped", threadId=17)
        await until(pilot, lambda: app.query_one("#source", DataTable).row_count == 2)
        assert calls(session, "source")[-1] == {"source": session.source, "sourceReference": 9}
        assert app.query_one("#breakpoint", Button).disabled
        session.failures["evaluate"] = "[bold]evaluation denied[/bold]"
        await pilot.press("ctrl+e", "x", "enter")
        await until(pilot, lambda: "[bold]evaluation denied[/bold]" in log_text(app, "diagnostics"))
        await pilot.resize_terminal(80, 24)
        await pilot.pause()
        assert not app.query_one("#inspectors").display
        assert app.query_one("#expression").region.bottom < app.size.height
        app.query_one("#view-tabs", Tabs).active = "stack-view"
        await pilot.pause()
        assert app.query_one("#stack").region.height >= 2
        assert not app.query_one("#source-pane").display
        await pilot.click("#help")
        assert isinstance(app.screen, HelpScreen)
        await pilot.click("#help-close")
        session.close_gate.clear()
        await pilot.press("ctrl+d")
        await until(pilot, lambda: session.close_calls == 1)
        await pilot.press("ctrl+q")
        session.close_gate.set()
        await until(pilot, lambda: session.state == "closed")
    assert session.close_calls == 1


async def test_start_failure_is_actionable_and_app_remains_responsive(session: FakeSession) -> None:
    session.failures["start"] = "Install debugpy in the configured Python interpreter"
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(80, 32)) as pilot:
        await until(pilot, lambda: "Install debugpy" in log_text(app, "diagnostics"))
        assert app.query_one("#continue", Button).disabled
        assert "failed" in str(app.query_one("#status", Static).render())
        await pilot.press("f1")
        assert isinstance(app.screen, HelpScreen)
        await pilot.press("escape", "ctrl+q")
    assert session.close_calls == 1


async def test_failed_step_recovers_after_thread_selection(session: FakeSession) -> None:
    gate = asyncio.Event()
    session.blocked["next"] = gate
    session.failures["next"] = "Step rejected"
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await stopped(app, pilot, session)
        await pilot.press("f10")
        await until(pilot, lambda: bool(calls(session, "next")))
        threads = app.query_one("#threads", DataTable)
        threads.focus()
        threads.move_cursor(row=1)
        await pilot.press("enter")
        gate.set()
        await until(pilot, lambda: "Step rejected" in log_text(app, "diagnostics"))
        await until(pilot, lambda: not app.query_one("#continue", Button).disabled)
        await until(pilot, lambda: calls(session, "scopes")[-1] == {"frameId": 303})
        assert session.state == "stopped"


async def test_disconnect_shortcut_takes_priority_in_expression_input(session: FakeSession) -> None:
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await stopped(app, pilot, session)
        await pilot.press("ctrl+e", "a", "b", "home", "ctrl+d")
        await until(pilot, lambda: session.state == "closed")
        assert app.query_one("#expression", Input).value == "ab"
    assert session.close_calls == 1


async def test_help_does_not_interrupt_live_updates(session: FakeSession) -> None:
    app = DebuggerApp(session.config, session=session)
    async with app.run_test(size=(120, 42)) as pilot:
        await until(pilot, lambda: bool(calls(session, "threads")))
        await pilot.press("f1")
        session.emit("stopped", threadId=17)
        session.emit("output", output="Output while help is open")
        await until(pilot, lambda: bool(calls(session, "scopes")))
        assert isinstance(app.screen, HelpScreen)
        await pilot.press("escape")
        await until(pilot, lambda: app.query_one("#source", DataTable).row_count == 3)
        await until(pilot, lambda: "Output while help is open" in log_text(app, "output"))
        assert not app.query_one("#continue", Button).disabled
