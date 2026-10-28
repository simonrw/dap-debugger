"""Textual interface for one Debug Adapter Protocol session."""

from __future__ import annotations

import asyncio
import re
from pathlib import Path
from typing import Any, Coroutine, Protocol

from rich.text import Text
from textual import on
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, HorizontalScroll, Vertical
from textual.screen import ModalScreen, Screen
from textual.widgets import (
    Button,
    DataTable,
    Input,
    RichLog,
    Static,
    TabbedContent,
    TabPane,
    Tree,
)

from dap_debugger.config import SessionConfig
from dap_debugger.session import DebugSession

_ANSI = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-_]")
_CONTROLS = re.compile(r"[\x00-\x08\x0b-\x1f\x7f-\x9f]")


def literal(value: object) -> Text:
    """Make adapter text inert, including ANSI escapes and control characters."""
    return Text(_CONTROLS.sub("", _ANSI.sub("", str(value))))


class HelpScreen(ModalScreen[None]):
    BINDINGS = [("escape", "dismiss", "Close help"), ("f1", "dismiss", "Close help")]
    DEFAULT_CSS = """
    HelpScreen { align: center middle; }
    #help-box { width: 70; max-width: 95%; height: auto; max-height: 100%; overflow-y: auto; padding: 1 2; border: round $accent; background: $surface; }
    #help-text { height: auto; }
    #help-close { margin-top: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical(id="help-box"):
            yield Static(
                "F5 Continue   F6 Pause   F9 Breakpoint\n"
                "F10 Step over   F11 Step into   Shift+F11 Step out\n"
                "F2 Stack   F3 Variables   F4 Output\n"
                "Ctrl+E Expression   Escape Back to code\n"
                "Ctrl+D Disconnect   Ctrl+Q Quit\n\n"
                "Tab / Shift+Tab move focus; arrows / Enter select or expand.\n"
                "Hover over buttons for shortcuts.\n\n"
                "Evaluation runs in the selected frame and can modify the target.\n\n"
                "Breakpoints: ? pending, B verified, ! rejected, R relocated.\n"
                "> marks the execution line.\n\n"
                "Disconnect stops launches and detaches attached targets.\n"
                "Paused attached targets may remain paused.",
                id="help-text",
                markup=False,
            )
            yield Button("Close help", id="help-close")

    @on(Button.Pressed, "#help-close")
    def close_help(self) -> None:
        self.dismiss()


class Session(Protocol):
    """Session operations used by the debugger interface."""

    @property
    def state(self) -> str: ...

    async def start(self) -> None: ...

    async def next_event(self) -> dict[str, Any]: ...

    async def request(
        self, command: str, arguments: dict[str, Any] | None = None
    ) -> dict[str, Any]: ...

    async def set_breakpoints(self, path: str, lines: list[int]) -> list[dict[str, Any]]: ...

    async def close(self, *, terminate: bool | None = None) -> None: ...


class DebuggerApp(App[None]):
    """Inspect and control a session, with requests running outside input handlers."""

    TITLE = "DAP Debugger"
    AUTO_FOCUS = "#source"
    BINDINGS = [
        Binding("f2", "stack", "Stack"),
        Binding("f3", "variables", "Variables"),
        Binding("f4", "output", "Output"),
        Binding("escape", "source", "Back to code"),
        Binding("f5", "continue", "Continue"),
        Binding("f6", "pause", "Pause"),
        Binding("f9", "breakpoint", "Breakpoint"),
        Binding("f10", "step_over", "Over"),
        Binding("f11", "step_into", "Into"),
        Binding("shift+f11", "step_out", "Out"),
        Binding("ctrl+e", "evaluate", "Expression"),
        Binding("ctrl+d", "disconnect", "Disconnect", priority=True),
        Binding("ctrl+q", "quit", "Quit", priority=True),
        Binding("f1", "help", "Help"),
    ]
    CSS = """
    Screen { layout: vertical; }
    #frame { height: 1fr; border: round $panel; }
    #actions { height: 1; scrollbar-size: 0 0; background: $surface; }
    #actions Button, #actions Button:hover, #actions Button.-active {
        width: auto; min-width: 3; height: 1; border: none; padding: 0;
        text-style: none;
    }
    #actions Button:focus { text-style: bold reverse; }
    #status { height: 1; color: $text-muted; padding: 0 1; }
    #workspace { height: 1fr; min-height: 3; }
    #source-pane { width: 1fr; }
    #source-title { height: 1; padding: 0 1; color: $text-muted; }
    #source { height: 1fr; }
    #inspectors { display: none; width: 1fr; min-width: 28; border-left: solid $panel; }
    .inspecting #source-pane { width: 2fr; }
    .inspecting #inspectors { display: block; }
    #inspectors ContentSwitcher { height: 1fr; }
    #inspectors TabPane { padding: 0; }
    #threads { height: 5; }
    #stack, #variables { height: 1fr; }
    #logs { display: none; height: 30%; min-height: 5; border-top: solid $panel; }
    .logging #logs { display: block; }
    #logs ContentSwitcher { height: 1fr; }
    #logs TabPane { padding: 0; }
    RichLog { height: 1fr; }
    #evaluate-row { display: none; height: 3; }
    .evaluating #evaluate-row { display: block; }
    #expression { width: 1fr; }
    #evaluate { width: 10; min-width: 10; }
    .narrow.inspecting #source-pane { display: none; }
    .narrow #inspectors { width: 1fr; border-left: none; }
    .narrow #threads { height: 3; }
    """

    def __init__(self, config: SessionConfig, *, session: Session | None = None) -> None:
        super().__init__()
        self.config = config
        self.session = session if session is not None else DebugSession(config)
        self._session_state = "new"
        self._generation = 0
        self._selected_thread_id: int | None = None
        self._frame_id: int | None = None
        self._stack_frames: dict[int, dict[str, Any]] = {}
        self._source_path: str | None = None
        self._source_lines: list[str] = []
        self._current_line: int | None = None
        self._desired_breakpoints = {
            path: sorted(set(lines)) for path, lines in config.breakpoints.items()
        }
        self._breakpoint_records: dict[str, list[dict[str, Any]]] = {}
        self._breakpoint_versions: dict[str, int] = {}
        self._breakpoint_locks: dict[str, asyncio.Lock] = {}
        self._command_busy = False
        self._startup_complete = False
        self._disconnecting = False
        self._quit_requested = False
        self._session_close_task: asyncio.Task[None] | None = None

    def compose(self) -> ComposeResult:
        with Vertical(id="frame"):
            with HorizontalScroll(id="actions"):
                for label, action, tooltip in [
                    ("Run", "continue", "Continue (F5)"),
                    ("Pause", "pause", "Pause (F6)"),
                    ("Over", "step-over", "Step over (F10)"),
                    ("Into", "step-into", "Step into (F11)"),
                    ("Out", "step-out", "Step out (Shift+F11)"),
                    ("BP", "breakpoint", "Toggle breakpoint (F9)"),
                    ("Stack", "stack-focus", "Toggle threads / stack (F2)"),
                    ("Vars", "variables-focus", "Toggle variables (F3)"),
                    ("Log", "output-focus", "Toggle output (F4)"),
                    ("Eval", "expression-focus", "Evaluate expression (Ctrl+E)"),
                    ("Stop", "disconnect", "Disconnect attached target / stop launch (Ctrl+D)"),
                    ("?", "help", "Help (F1); quit with Ctrl+Q"),
                ]:
                    yield Button(label, id=action, tooltip=tooltip)
            with Horizontal(id="workspace"):
                with Vertical(id="source-pane"):
                    yield Static(
                        "Source appears when the target stops. F1 for help.",
                        id="source-title",
                        markup=False,
                    )
                    yield DataTable(
                        id="source", cursor_type="row", show_header=False, show_row_labels=False
                    )
                with TabbedContent(id="inspectors"):
                    with TabPane("Threads / stack", id="stack-tab"):
                        yield DataTable(id="threads", cursor_type="row", show_row_labels=False)
                        yield DataTable(id="stack", cursor_type="row", show_row_labels=False)
                    with TabPane("Variables", id="variables-tab"):
                        yield Tree("Scopes appear when stopped", id="variables")
            with TabbedContent(id="logs"):
                with TabPane("Output", id="output-tab"):
                    yield RichLog(
                        id="output", max_lines=2000, markup=False, highlight=False, wrap=True
                    )
                with TabPane("Results", id="results-tab"):
                    yield RichLog(
                        id="results", max_lines=1000, markup=False, highlight=False, wrap=True
                    )
                with TabPane("Diagnostics", id="diagnostics-tab"):
                    yield RichLog(
                        id="diagnostics", max_lines=1000, markup=False, highlight=False, wrap=True
                    )
            with Horizontal(id="evaluate-row"):
                yield Input(
                    placeholder="Expression (can modify target); Enter to evaluate", id="expression"
                )
                yield Button("Evaluate", id="evaluate", disabled=True)
            yield Static(literal(f"{self.config.name} | Ready to connect | F1 Help"), id="status")

    @property
    def _debug_screen(self) -> Screen:
        return self.screen_stack[0]

    def on_mount(self) -> None:
        self._debug_screen.query_one("#source", DataTable).add_columns("", "Line", "Source")
        self._debug_screen.query_one("#threads", DataTable).add_columns("Thread", "Name")
        self._debug_screen.query_one("#stack", DataTable).add_columns("Frame", "Location")
        self._debug_screen.set_class(self.size.width < 100, "narrow")
        self._set_state("starting")
        self._spawn(self._consume_events())
        self._spawn(self._start_session())

    def on_resize(self) -> None:
        self._debug_screen.set_class(self.size.width < 100, "narrow")

    def _toggle_inspector(self, pane: str, focus: str) -> None:
        screen = self._debug_screen
        inspectors = screen.query_one("#inspectors", TabbedContent)
        if screen.has_class("inspecting") and inspectors.active == pane:
            screen.remove_class("inspecting")
            screen.query_one("#source").focus()
        else:
            inspectors.active = pane
            screen.add_class("inspecting")
            self.call_after_refresh(screen.query_one(focus).focus)

    def action_stack(self) -> None:
        self._toggle_inspector("stack-tab", "#stack")

    def action_variables(self) -> None:
        self._toggle_inspector("variables-tab", "#variables")

    def action_output(self) -> None:
        screen = self._debug_screen
        if screen.has_class("logging"):
            screen.remove_class("logging", "evaluating")
            focus = "#source"
            if screen.has_class("narrow") and screen.has_class("inspecting"):
                inspectors = screen.query_one("#inspectors", TabbedContent)
                focus = "#stack" if inspectors.active == "stack-tab" else "#variables"
            screen.query_one(focus).focus()
        else:
            screen.add_class("logging")
            screen.query_one("#logs", TabbedContent).active = "output-tab"
            self.call_after_refresh(screen.query_one("#output").focus)

    def action_source(self) -> None:
        self._debug_screen.remove_class("inspecting", "logging", "evaluating")
        self._debug_screen.query_one("#source").focus()

    def _spawn(self, operation: Coroutine[Any, Any, None]) -> None:
        self.run_worker(self._guard(operation), exit_on_error=False)

    async def _guard(self, operation: Coroutine[Any, Any, None]) -> None:
        try:
            await operation
        except Exception as error:
            self._diagnostic(str(error))

    def _diagnostic(self, message: str) -> None:
        self._debug_screen.add_class("logging")
        self._debug_screen.query_one("#diagnostics", RichLog).write(literal(message))
        self._debug_screen.query_one("#logs", TabbedContent).active = "diagnostics-tab"

    def _set_state(self, state: str) -> None:
        self._session_state = state
        self._debug_screen.query_one("#status", Static).update(
            literal(f"{self.config.name} | {state} | F1 Help")
        )
        self._update_actions()

    def _update_actions(self) -> None:
        stopped = (
            self._session_state == "stopped"
            and self._selected_thread_id is not None
            and not self._disconnecting
        )
        running = (
            self._session_state == "running"
            and self._selected_thread_id is not None
            and not self._disconnecting
        )
        for action in ["continue", "step-over", "step-into", "step-out"]:
            self._debug_screen.query_one(f"#{action}", Button).disabled = (
                not stopped or self._command_busy
            )
        self._debug_screen.query_one("#pause", Button).disabled = not running or self._command_busy
        self._debug_screen.query_one("#evaluate", Button).disabled = (
            not stopped or self._frame_id is None
        )
        self._debug_screen.query_one("#breakpoint", Button).disabled = (
            self._source_path is None or self._disconnecting
        )
        self._debug_screen.query_one("#disconnect", Button).disabled = (
            self._disconnecting or self._session_state == "closed"
        )

    def _current(self, generation: int, *, stopped: bool = True) -> bool:
        states = ["stopped"] if stopped else ["stopped", "running"]
        return (
            generation == self._generation
            and self._session_state in states
            and not self._disconnecting
        )

    def _clear_inspection(self, *, source: bool = False) -> None:
        self._frame_id = None
        self._stack_frames.clear()
        self._debug_screen.query_one("#stack", DataTable).clear()
        self._debug_screen.query_one("#variables", Tree).reset("Scopes appear when stopped")
        self._current_line = None
        if source:
            self._source_path = None
            self._source_lines = []
            self._debug_screen.query_one("#source-title", Static).update("Loading source...")
        self._render_source()
        self._update_actions()

    async def _start_session(self) -> None:
        try:
            await self.session.start()
        except Exception as error:
            if not self._disconnecting:
                self._set_state("failed")
                self._diagnostic(f"Could not start session: {error}")
            return
        if self._disconnecting:
            return
        self._startup_complete = True
        if self._session_state == "starting":
            self._set_state(self.session.state)
        if self._session_state == "running":
            self._spawn(self._load_threads(self._selected_thread_id, self._generation))
        for path in self._desired_breakpoints:
            self._spawn(self._apply_breakpoints(path, self._breakpoint_versions.get(path, 0)))

    async def _consume_events(self) -> None:
        while True:
            event = await self.session.next_event()
            body = event.get("body", {})
            kind = event.get("event")
            if kind == "output":
                category = body.get("category", "console")
                log = (
                    "diagnostics"
                    if category in ["telemetry", "adapter", "diagnostic"]
                    else "output"
                )
                self._debug_screen.query_one(f"#{log}", RichLog).write(
                    literal(body.get("output", ""))
                )
            elif kind == "sessionError":
                self._diagnostic(body.get("message", "Session failed"))
            elif kind == "stopped" and not self._disconnecting:
                self._begin_stop(body.get("threadId"))
            elif kind in ["continued", "terminated", "exited"]:
                self._generation += 1
                self._set_state("running" if kind == "continued" else "terminated")
                self._clear_inspection()
            elif kind == "sessionState":
                state = body["state"]
                if (
                    state in ["running", "terminated", "failed", "closed"]
                    and state != self._session_state
                ):
                    self._generation += 1
                    self._clear_inspection()
                self._set_state(state)
                if state == "closed":
                    return
            elif kind == "thread" and self._session_state in ["running", "stopped"]:
                self._spawn(
                    self._load_threads(self._selected_thread_id, self._generation, stack=False)
                )
            elif kind == "breakpoint":
                record = body.get("breakpoint", {})
                for records in self._breakpoint_records.values():
                    for old in records:
                        if record.get("id") is not None and old.get("id") == record["id"]:
                            old.update(record)
                self._render_source()

    def _begin_stop(self, thread_id: int | None) -> None:
        self._generation += 1
        self._selected_thread_id = thread_id
        self._set_state("stopped")
        self._clear_inspection(source=True)
        self._spawn(self._load_threads(thread_id, self._generation))

    async def _load_threads(
        self, preferred: int | None, generation: int, *, stack: bool = True
    ) -> None:
        response = await self.session.request("threads", {})
        if not self._current(generation, stopped=False):
            return
        threads = response.get("threads", [])
        table = self._debug_screen.query_one("#threads", DataTable)
        table.clear()
        for thread in threads:
            table.add_row(literal(thread["id"]), literal(thread["name"]), key=str(thread["id"]))
        ids = [thread["id"] for thread in threads]
        self._selected_thread_id = preferred if preferred in ids else (ids[0] if ids else preferred)
        if self._selected_thread_id in ids:
            table.move_cursor(row=ids.index(self._selected_thread_id))
        self._update_actions()
        if stack and self._session_state == "stopped" and self._selected_thread_id is not None:
            await self._load_stack(self._selected_thread_id, generation)

    async def _load_stack(self, thread_id: int, generation: int) -> None:
        response = await self.session.request(
            "stackTrace", {"threadId": thread_id, "startFrame": 0, "levels": 100}
        )
        if not self._current(generation):
            return
        frames = response.get("stackFrames", [])
        self._stack_frames = {frame["id"]: frame for frame in frames}
        table = self._debug_screen.query_one("#stack", DataTable)
        table.clear()
        for frame in frames:
            source = frame.get("source") or {}
            location = f"{source.get('name', source.get('path', '?'))}:{frame.get('line', '?')}"
            table.add_row(literal(frame["name"]), literal(location), key=str(frame["id"]))
        if frames:
            self._select_frame(frames[0])
        else:
            self._debug_screen.query_one("#source-title", Static).update(
                "No stack frames available for this thread."
            )

    @on(DataTable.RowSelected, "#threads")
    def select_thread(self, event: DataTable.RowSelected) -> None:
        if event.row_key.value is None:
            return
        self._selected_thread_id = int(event.row_key.value)
        self._generation += 1
        self._clear_inspection(source=True)
        if self._session_state == "stopped":
            self._spawn(self._load_stack(self._selected_thread_id, self._generation))

    @on(DataTable.RowSelected, "#stack")
    def select_frame(self, event: DataTable.RowSelected) -> None:
        if event.row_key.value is None:
            return
        frame = self._stack_frames.get(int(event.row_key.value))
        if frame is not None and self._session_state == "stopped":
            self._select_frame(frame)

    def _select_frame(self, frame: dict[str, Any]) -> None:
        self._generation += 1
        self._frame_id = frame["id"]
        self._source_path = None
        self._source_lines = []
        self._current_line = frame.get("line")
        self._debug_screen.query_one("#source", DataTable).clear()
        self._debug_screen.query_one("#source-title", Static).update("Loading source...")
        self._debug_screen.query_one("#variables", Tree).reset("Loading scopes...")
        self._update_actions()
        self._spawn(self._load_source(frame, self._generation))
        self._spawn(self._load_scopes(frame["id"], self._generation))

    async def _load_source(self, frame: dict[str, Any], generation: int) -> None:
        source = frame.get("source") or {}
        path = source.get("path")
        content = None
        local_path = None
        problem = "No source path supplied"
        if path:
            candidate = Path(path).absolute()
            try:
                content = await asyncio.to_thread(
                    candidate.read_text, encoding="utf-8", errors="replace"
                )
                local_path = str(candidate)
            except (OSError, ValueError) as error:
                problem = f"Unavailable source: {path} ({error})"
        if not self._current(generation):
            return
        if content is None and source.get("sourceReference", 0) > 0:
            self._debug_screen.query_one("#source-title", Static).update(
                literal(f"{problem}; requesting adapter source...")
            )
            try:
                response = await self.session.request(
                    "source", {"source": source, "sourceReference": source["sourceReference"]}
                )
            except Exception:
                if self._current(generation):
                    self._debug_screen.query_one("#source-title", Static).update(
                        literal(f"{problem}; adapter source request failed")
                    )
                raise
            content = response.get("content", "")
        if not self._current(generation):
            return
        self._source_path = local_path
        self._source_lines = content.splitlines() if content is not None else []
        title = local_path or (
            f"{path or source.get('name', 'Source')} (adapter source)"
            if content is not None
            else problem
        )
        self._debug_screen.query_one("#source-title", Static).update(literal(title))
        self._render_source()
        if self._source_lines:
            self._debug_screen.query_one("#source", DataTable).move_cursor(
                row=max(0, min((self._current_line or 1) - 1, len(self._source_lines) - 1))
            )
        self._update_actions()

    async def _load_scopes(self, frame_id: int, generation: int) -> None:
        response = await self.session.request("scopes", {"frameId": frame_id})
        if not self._current(generation):
            return
        tree = self._debug_screen.query_one("#variables", Tree)
        tree.reset("Scopes")
        tree.root.expand()
        for scope in response.get("scopes", []):
            self._add_variable(tree.root, scope, generation)

    def _add_variable(self, parent: Any, variable: dict[str, Any], generation: int) -> None:
        reference = variable.get("variablesReference", 0)
        label = str(variable.get("name", ""))
        if "value" in variable:
            label += f" = {variable['value']}"
        if variable.get("type"):
            label += f" ({variable['type']})"
        parent.add(
            literal(label),
            data={"reference": reference, "generation": generation, "loaded": False},
            allow_expand=reference > 0,
        )

    @on(Tree.NodeExpanded, "#variables")
    def expand_variable(self, event: Tree.NodeExpanded) -> None:
        data = event.node.data
        if data and data["reference"] and not data["loaded"] and self._current(data["generation"]):
            data["loaded"] = True
            self._spawn(self._load_variables(event.node, data))

    async def _load_variables(self, node: Any, data: dict[str, Any]) -> None:
        try:
            response = await self.session.request(
                "variables", {"variablesReference": data["reference"]}
            )
        except Exception:
            data["loaded"] = False
            raise
        if self._current(data["generation"]):
            for variable in response.get("variables", []):
                self._add_variable(node, variable, data["generation"])

    def _render_source(self) -> None:
        table = self._debug_screen.query_one("#source", DataTable)
        selected = table.cursor_row
        table.clear()
        markers = {}
        desired = self._desired_breakpoints.get(self._source_path, [])
        records = self._breakpoint_records.get(self._source_path, [])
        for index, line in enumerate(desired):
            if index >= len(records):
                markers[line] = "?"
                continue
            record = records[index]
            actual = record.get("line", line)
            if not record.get("verified"):
                markers[line] = "!"
            elif actual != line:
                markers[line] = "R"
                markers[actual] = "B"
            else:
                markers[line] = "B"
        for line, content in enumerate(self._source_lines, 1):
            execution = ">" if line == self._current_line else " "
            table.add_row(
                Text(execution + markers.get(line, " ")),
                Text(str(line)),
                literal(content),
                key=str(line),
            )
        if self._source_lines:
            table.move_cursor(row=min(selected, len(self._source_lines) - 1))

    def action_breakpoint(self) -> None:
        if self._source_path is None or not self._source_lines or self._disconnecting:
            return
        path = self._source_path
        line = self._debug_screen.query_one("#source", DataTable).cursor_row + 1
        lines = set(self._desired_breakpoints.get(path, []))
        if line in lines:
            lines.remove(line)
        else:
            lines.add(line)
        self._desired_breakpoints[path] = sorted(lines)
        self._breakpoint_records.pop(path, None)
        version = self._breakpoint_versions.get(path, 0) + 1
        self._breakpoint_versions[path] = version
        self._render_source()
        self._spawn(self._apply_breakpoints(path, version))

    async def _apply_breakpoints(self, path: str, version: int) -> None:
        lock = self._breakpoint_locks.setdefault(path, asyncio.Lock())
        async with lock:
            if self._breakpoint_versions.get(path, 0) != version or self._disconnecting:
                return
            lines = list(self._desired_breakpoints[path])
            records = await self.session.set_breakpoints(path, lines)
            if self._breakpoint_versions.get(path, 0) != version or not self._startup_complete:
                return
            self._breakpoint_records[path] = records
            self._render_source()
            for line, record in zip(lines, records):
                if not record.get("verified") or record.get("line", line) != line:
                    status = (
                        "rejected"
                        if not record.get("verified")
                        else f"relocated to {record['line']}"
                    )
                    self._diagnostic(
                        f"Breakpoint {path}:{line}: {status}. {record.get('message', '')}"
                    )

    def _execution(self, command: str) -> None:
        expected = "running" if command == "pause" else "stopped"
        if (
            self._session_state != expected
            or self._selected_thread_id is None
            or self._command_busy
            or self._disconnecting
        ):
            return
        self._command_busy = True
        if command != "pause":
            self._generation += 1
            self._set_state("running")
            self._clear_inspection()
        self._update_actions()
        self._spawn(self._send_execution(command, self._selected_thread_id))

    async def _send_execution(self, command: str, thread_id: int) -> None:
        try:
            await self.session.request(command, {"threadId": thread_id})
        except Exception as error:
            self._diagnostic(f"{command}: {error}")
            if (
                command != "pause"
                and self._session_state == "running"
                and self.session.state == "stopped"
                and not self._disconnecting
            ):
                self._begin_stop(self._selected_thread_id or thread_id)
        finally:
            self._command_busy = False
            self._update_actions()

    def action_continue(self) -> None:
        self._execution("continue")

    def action_pause(self) -> None:
        self._execution("pause")

    def action_step_over(self) -> None:
        self._execution("next")

    def action_step_into(self) -> None:
        self._execution("stepIn")

    def action_step_out(self) -> None:
        self._execution("stepOut")

    def action_evaluate(self) -> None:
        self._debug_screen.add_class("logging", "evaluating")
        self._debug_screen.query_one("#logs", TabbedContent).active = "results-tab"
        self.call_after_refresh(self._debug_screen.query_one("#expression", Input).focus)

    @on(Input.Submitted, "#expression")
    def evaluate_expression(self) -> None:
        expression = self._debug_screen.query_one("#expression", Input).value
        if (
            expression.strip()
            and self._session_state == "stopped"
            and self._frame_id is not None
            and not self._disconnecting
        ):
            self._spawn(self._evaluate(expression, self._frame_id, self._generation))

    async def _evaluate(self, expression: str, frame_id: int, generation: int) -> None:
        response = await self.session.request(
            "evaluate", {"expression": expression, "frameId": frame_id, "context": "repl"}
        )
        if not self._current(generation):
            return
        self._debug_screen.query_one("#results", RichLog).write(
            literal(f"> {expression}\n{response.get('result', '')}")
        )
        self._debug_screen.add_class("logging")
        self._debug_screen.query_one("#logs", TabbedContent).active = "results-tab"
        if response.get("variablesReference", 0):
            variable = {**response, "name": expression, "value": response.get("result", "")}
            self._add_variable(
                self._debug_screen.query_one("#variables", Tree).root, variable, generation
            )

    @on(Button.Pressed)
    def button_action(self, event: Button.Pressed) -> None:
        actions = {
            "stack-focus": self.action_stack,
            "variables-focus": self.action_variables,
            "output-focus": self.action_output,
            "continue": self.action_continue,
            "pause": self.action_pause,
            "step-over": self.action_step_over,
            "step-into": self.action_step_into,
            "step-out": self.action_step_out,
            "breakpoint": self.action_breakpoint,
            "evaluate": self.evaluate_expression,
            "expression-focus": self.action_evaluate,
            "disconnect": self.action_disconnect,
            "help": self.action_help,
        }
        action = actions.get(event.button.id)
        if action:
            action()

    def action_help(self) -> None:
        self.push_screen(HelpScreen())

    def action_disconnect(self) -> None:
        if not self._disconnecting:
            self._spawn(self._disconnect_session())

    async def action_quit(self) -> None:
        if not self._quit_requested:
            self._quit_requested = True
            self._spawn(self._disconnect_session(quit_app=True))

    async def _disconnect_session(self, *, quit_app: bool = False) -> None:
        self._disconnecting = True
        self._generation += 1
        self._set_state("disconnecting")
        self._clear_inspection()
        await self._close_session()
        self._set_state("closed")
        if quit_app:
            self.exit()

    async def _close_session(self) -> None:
        self._disconnecting = True
        if self._session_close_task is None:
            self._session_close_task = asyncio.create_task(self._bounded_close())
        await asyncio.shield(self._session_close_task)

    async def _bounded_close(self) -> None:
        closing = asyncio.create_task(self.session.close())
        try:
            await asyncio.wait_for(asyncio.shield(closing), timeout=5)
        except Exception as error:
            closing.cancel()
            if self.screen_stack and self.is_mounted(self._debug_screen):
                self._diagnostic(f"Disconnect failed: {error or 'timed out'}")

    async def on_unmount(self) -> None:
        await self._close_session()
