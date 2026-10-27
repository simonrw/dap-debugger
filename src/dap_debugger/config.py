"""Validated configuration for one debugger connection."""

from __future__ import annotations

import copy
import json
import math
import os
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, TypedDict


def _string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be a nonempty string")
    return value


def _local_path(value: Any, name: str, base: Path) -> str:
    path = Path(_string(value, name)).expanduser()
    return str((base / path).resolve())


def _executable(value: Any, name: str, base: Path) -> str:
    value = _string(value, name)
    if os.sep in value or (os.altsep and os.altsep in value):
        # Resolving interpreter symlinks would discard the virtual environment.
        return os.path.abspath(base / Path(value).expanduser())
    return value


def _seconds(value: Any, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be positive seconds")
    if not math.isfinite(value) or value <= 0:
        raise ValueError(f"{name} must be positive finite seconds")
    return float(value)


def validate_lines(lines: Any) -> list[int]:
    if not isinstance(lines, list) or any(type(line) is not int or line <= 0 for line in lines):
        raise ValueError("breakpoint lines must be a list of positive integers")
    return sorted(set(lines))


class ConnectEndpoint(TypedDict):
    host: str
    port: int


@dataclass
class SessionConfig:
    name: str = "Debug session"
    adapter: str = "python"
    request: str = "launch"
    python: str = sys.executable
    dlv: str = "dlv"
    connect: ConnectEndpoint | None = None
    arguments: dict[str, Any] = field(default_factory=dict)
    breakpoints: dict[str, list[int]] = field(default_factory=dict)
    path_mappings: list[dict[str, str]] = field(default_factory=list)
    request_timeout: float = 15
    startup_timeout: float = 60

    @classmethod
    def from_dict(cls, data: dict, *, base_dir: Path) -> SessionConfig:
        if not isinstance(data, dict):
            raise ValueError("configuration must be a JSON object")
        allowed = {
            "name",
            "adapter",
            "request",
            "python",
            "dlv",
            "connect",
            "arguments",
            "breakpoints",
            "pathMappings",
            "requestTimeout",
            "startupTimeout",
        }
        unknown = data.keys() - allowed
        if unknown:
            raise ValueError(f"unknown configuration keys: {', '.join(sorted(unknown))}")
        adapter = data.get("adapter")
        request = data.get("request")
        if adapter not in ("python", "go"):
            raise ValueError("adapter must be 'python' or 'go'")
        if request not in ("launch", "attach"):
            raise ValueError("request must be 'launch' or 'attach'")
        base = Path(base_dir).resolve()
        connect: ConnectEndpoint | None = None
        if request == "launch" and "connect" in data:
            raise ValueError("connect is forbidden for launch")
        if request == "attach":
            endpoint = data.get("connect")
            if not isinstance(endpoint, dict) or set(endpoint) != {"host", "port"}:
                raise ValueError("attach requires connect with host and port")
            host = _string(endpoint["host"], "connect.host")
            port = endpoint["port"]
            if type(port) is not int or not 1 <= port <= 65535:
                raise ValueError("connect.port must be an integer from 1 to 65535")
            connect = {"host": host, "port": port}
        arguments = copy.deepcopy(data.get("arguments", {}))
        if not isinstance(arguments, dict):
            raise ValueError("arguments must be an object")
        if "args" in arguments and (
            not isinstance(arguments["args"], list)
            or any(not isinstance(arg, str) for arg in arguments["args"])
        ):
            raise ValueError("arguments.args must be a list of strings")
        if request == "launch":
            if "cwd" in arguments:
                arguments["cwd"] = _local_path(arguments["cwd"], "arguments.cwd", base)
            else:
                arguments["cwd"] = str(base)
            if adapter == "python":
                if ("program" in arguments) == ("module" in arguments):
                    raise ValueError("Python launch requires exactly one of program and module")
                if "module" in arguments:
                    _string(arguments["module"], "arguments.module")
            else:
                if arguments.setdefault("mode", "debug") not in ("debug", "exec", "test"):
                    raise ValueError("Go launch mode must be debug, exec, or test")
                if "program" not in arguments:
                    raise ValueError("Go launch requires arguments.program")
            if "program" in arguments:
                arguments["program"] = _local_path(arguments["program"], "arguments.program", base)
        else:
            if "processId" in arguments:
                raise ValueError(
                    "TCP attach joins an existing debugger; arguments.processId is not supported"
                )
            if adapter == "go":
                if arguments.setdefault("mode", "remote") != "remote":
                    raise ValueError("Go TCP attach requires arguments.mode 'remote'")
                local_options = {"waitFor", "waitForInterval", "waitForDuration", "processName"}
                if local_options.intersection(arguments):
                    raise ValueError(
                        "Go remote attach does not support local process selection arguments"
                    )
        if adapter == "python":
            if arguments.get("console", "internalConsole") != "internalConsole":
                raise ValueError("only the internalConsole console is supported")
            if arguments.get("subProcess", False) is not False:
                raise ValueError("Python subprocess debugging is not supported")
            arguments["subProcess"] = False
            if request == "launch":
                arguments.update(console="internalConsole", redirectOutput=True)
        raw_breakpoints = data.get("breakpoints", {})
        if not isinstance(raw_breakpoints, dict):
            raise ValueError("breakpoints must be an object")
        breakpoints = {
            _local_path(path, "breakpoint path", base): validate_lines(lines)
            for path, lines in raw_breakpoints.items()
        }
        mappings = copy.deepcopy(data.get("pathMappings", []))
        if not isinstance(mappings, list):
            raise ValueError("pathMappings must be a list")
        for mapping in mappings:
            if not isinstance(mapping, dict) or set(mapping) != {"localRoot", "remoteRoot"}:
                raise ValueError("each path mapping requires localRoot and remoteRoot")
            mapping["localRoot"] = _local_path(mapping["localRoot"], "localRoot", base)
            _string(mapping["remoteRoot"], "remoteRoot")
        mapping_key = "pathMappings" if adapter == "python" else "substitutePath"
        if mappings:
            if mapping_key in arguments:
                raise ValueError(f"top-level pathMappings conflicts with arguments.{mapping_key}")
            arguments[mapping_key] = (
                copy.deepcopy(mappings)
                if adapter == "python"
                else [{"from": item["localRoot"], "to": item["remoteRoot"]} for item in mappings]
            )
        return cls(
            name=_string(data.get("name", "Debug session"), "name"),
            adapter=adapter,
            request=request,
            python=_executable(data.get("python", sys.executable), "python", base),
            dlv=_executable(data.get("dlv", "dlv"), "dlv", base),
            connect=connect,
            arguments=arguments,
            breakpoints=breakpoints,
            path_mappings=mappings,
            request_timeout=_seconds(data.get("requestTimeout", 15), "requestTimeout"),
            startup_timeout=_seconds(data.get("startupTimeout", 60), "startupTimeout"),
        )


def load_config(path: str | Path) -> SessionConfig:
    path = Path(path).expanduser().resolve()
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, ValueError) as error:
        raise ValueError(f"Cannot read configuration {path}: {error}") from error
    return SessionConfig.from_dict(data, base_dir=path.parent)
