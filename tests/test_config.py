import json
import os
import sys
from pathlib import Path

import pytest

from dap_debugger.config import SessionConfig, load_config


def test_executable_paths_preserve_virtualenv_symlinks(tmp_path):
    interpreter = tmp_path / "venv" / "bin" / "python"
    interpreter.parent.mkdir(parents=True)
    interpreter.symlink_to(sys.executable)
    config = SessionConfig.from_dict(
        {
            "adapter": "python",
            "request": "launch",
            "python": "./venv/bin/python",
            "dlv": "./venv/bin/python",
            "arguments": {"program": "app.py"},
        },
        base_dir=tmp_path,
    )
    assert config.python == str(interpreter)
    assert config.dlv == str(interpreter)
    default = SessionConfig.from_dict(
        {
            "adapter": "python",
            "request": "launch",
            "arguments": {"program": "app.py"},
        },
        base_dir=tmp_path,
    )
    assert default.python == os.path.abspath(sys.executable)


@pytest.mark.parametrize(
    "arguments",
    [
        {"mode": "local"},
        {"mode": "remote", "processId": 123},
        {"processId": 0},
        {"waitFor": "program"},
        {"waitForInterval": 1},
        {"waitForDuration": 1},
        {"processName": "program"},
    ],
)
def test_go_remote_attach_rejects_local_process_selection(tmp_path, arguments):
    with pytest.raises(ValueError, match="remote|processId"):
        SessionConfig.from_dict(
            {
                "adapter": "go",
                "request": "attach",
                "arguments": arguments,
                "connect": {"host": "127.0.0.1", "port": 5678},
            },
            base_dir=tmp_path,
        )


def test_launch_paths_defaults_and_input_are_independent(tmp_path):
    data = {
        "adapter": "python",
        "request": "launch",
        "python": "./venv/bin/python",
        "arguments": {"program": "app.py", "args": ["one"], "env": {"X": "value"}},
        "breakpoints": {"app.py": [5, 2, 5]},
        "pathMappings": [{"localRoot": ".", "remoteRoot": "/target/src"}],
    }
    path = tmp_path / "debug.json"
    path.write_text(json.dumps(data))
    config = load_config(path)
    assert config.arguments["program"] == str(tmp_path / "app.py")
    assert config.arguments["cwd"] == str(tmp_path)
    assert config.python == str(tmp_path / "venv/bin/python")
    assert config.arguments["console"] == "internalConsole"
    assert config.arguments["redirectOutput"] is True
    assert config.arguments["subProcess"] is False
    assert config.breakpoints == {str(tmp_path / "app.py"): [2, 5]}
    config = SessionConfig.from_dict(data, base_dir=tmp_path)
    data["arguments"]["env"]["X"] = "changed"
    data["pathMappings"][0]["remoteRoot"] = "/changed"
    assert config.arguments["env"] == {"X": "value"}
    assert config.arguments["pathMappings"][0]["remoteRoot"] == "/target/src"


@pytest.mark.parametrize("adapter,key", [("python", "pathMappings"), ("go", "substitutePath")])
def test_attach_does_not_resolve_remote_paths_and_maps_once(tmp_path, adapter, key):
    config = SessionConfig.from_dict(
        {
            "adapter": adapter,
            "request": "attach",
            "connect": {"host": "localhost", "port": 5678},
            "arguments": {"program": "/remote/not-here", "cwd": "/remote"},
            "pathMappings": [{"localRoot": "local", "remoteRoot": "C:\\remote\\src"}],
        },
        base_dir=tmp_path,
    )
    assert config.arguments["program"] == "/remote/not-here"
    assert config.arguments["cwd"] == "/remote"
    expected = {"localRoot": str(tmp_path / "local"), "remoteRoot": "C:\\remote\\src"}
    if adapter == "go":
        expected = {"from": expected["localRoot"], "to": expected["remoteRoot"]}
        assert config.arguments["mode"] == "remote"
    assert config.arguments[key] == [expected]


@pytest.mark.parametrize(
    "change,match",
    [
        ({"unknown": 1}, "unknown"),
        ({"requestTimeout": True}, "requestTimeout"),
        ({"startupTimeout": float("inf")}, "startupTimeout"),
        ({"arguments": {"program": "a.py", "module": "a"}}, "exactly one"),
        ({"arguments": {}}, "exactly one"),
        ({"arguments": {"program": "a.py", "console": "integratedTerminal"}}, "internalConsole"),
        ({"arguments": {"program": "a.py", "subProcess": True}}, "subprocess"),
        ({"arguments": {"program": "a.py", "args": [1]}}, "args"),
        ({"breakpoints": {"a.py": [True]}}, "positive integers"),
        ({"breakpoints": {"a.py": [0]}}, "positive integers"),
        ({"connect": {"host": "localhost", "port": 1}}, "forbidden"),
        ({"pathMappings": [{"localRoot": "."}]}, "remoteRoot"),
        (
            {
                "pathMappings": [{"localRoot": ".", "remoteRoot": "/r"}],
                "arguments": {"program": "a.py", "pathMappings": []},
            },
            "conflicts",
        ),
    ],
)
def test_invalid_launch_config(change, match, tmp_path):
    with pytest.raises(ValueError, match=match):
        SessionConfig.from_dict(
            {"adapter": "python", "request": "launch", "arguments": {"program": "a.py"}, **change},
            base_dir=tmp_path,
        )


@pytest.mark.parametrize("port", [0, 65536, True, "5678"])
def test_attach_rejects_invalid_ports(port):
    with pytest.raises(ValueError, match="port"):
        SessionConfig.from_dict(
            {"adapter": "go", "request": "attach", "connect": {"host": "localhost", "port": port}},
            base_dir=Path.cwd(),
        )


def test_go_modes_and_missing_config(tmp_path):
    for mode in ["debug", "test", "exec"]:
        config = SessionConfig.from_dict(
            {"adapter": "go", "request": "launch", "arguments": {"program": ".", "mode": mode}},
            base_dir=tmp_path,
        )
        assert config.arguments["mode"] == mode
    with pytest.raises(ValueError, match="remote"):
        SessionConfig.from_dict(
            {
                "adapter": "go",
                "request": "attach",
                "connect": {"host": "localhost", "port": 5678},
                "arguments": {"mode": "local"},
            },
            base_dir=tmp_path,
        )
    with pytest.raises(ValueError, match="Cannot read configuration"):
        load_config(tmp_path / "missing.json")
