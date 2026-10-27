"""Drive the terminal interface through a real debugpy launch."""

import asyncio
import importlib.util
import sys

import pytest
from textual.widgets import Button, DataTable, RichLog

from dap_debugger.config import SessionConfig
from dap_debugger.ui import DebuggerApp

pytestmark = pytest.mark.integration


async def wait_until(pilot, predicate):
    async with asyncio.timeout(20):
        while not predicate():
            await pilot.pause(0.02)


async def test_python_launch_through_terminal_controls(tmp_path):
    if importlib.util.find_spec("debugpy") is None:
        pytest.skip("debugpy is not installed")
    program = tmp_path / "main.py"
    program.write_text("value = 41\nanswer = value + 1\nprint(answer)\n")
    config = SessionConfig.from_dict(
        {
            "adapter": "python",
            "request": "launch",
            "python": sys.executable,
            "arguments": {"program": str(program)},
            "breakpoints": {str(program): [2]},
        },
        base_dir=tmp_path,
    )
    app = DebuggerApp(config)
    async with app.run_test(size=(120, 42)) as pilot:
        await wait_until(pilot, lambda: not app.query_one("#evaluate", Button).disabled)
        await wait_until(pilot, lambda: app.query_one("#source", DataTable).row_count == 3)
        assert app.session.state == "stopped"
        await pilot.press("ctrl+e", *list("value + 1"), "enter")
        await wait_until(
            pilot,
            lambda: any("42" in line.text for line in app.query_one("#results", RichLog).lines),
        )
        source = app.query_one("#source", DataTable)
        source.focus()
        source.move_cursor(row=1)
        await pilot.press("f9")
        await wait_until(pilot, lambda: source.get_row("2")[0].plain == "> ")
        await pilot.press("f10")
        await wait_until(
            pilot,
            lambda: source.row_count == 3 and source.get_row("3")[0].plain.startswith(">"),
        )
        await pilot.press("f5")
        await wait_until(pilot, lambda: app.session.state == "terminated")
        await pilot.press("ctrl+q")
    assert app.session.state == "closed"
