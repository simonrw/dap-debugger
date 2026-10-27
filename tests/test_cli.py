"""Exercise the installed command without starting a terminal session."""

import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]


def run_cli(*args: str, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, "-m", "dap_debugger", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        timeout=10,
    )


def test_help():
    result = run_cli("--help")
    assert result.returncode == 0 and "--check" in result.stdout


@pytest.mark.parametrize(
    "filename", ["python-launch.json", "python-attach.json", "go-launch.json", "go-attach.json"]
)
def test_examples_validate_from_another_directory(filename, tmp_path):
    result = run_cli("--check", str(ROOT / "examples" / filename), cwd=tmp_path)
    assert result.returncode == 0, result.stderr


@pytest.mark.parametrize("contents", ["{", "[]", '{"adapter": "unknown"}'])
def test_invalid_config_is_a_usage_error(contents, tmp_path):
    config = tmp_path / "invalid.json"
    config.write_text(contents)
    result = run_cli("--check", str(config))
    assert result.returncode == 2 and "Traceback" not in result.stderr


def test_missing_config_is_a_usage_error(tmp_path):
    result = run_cli("--check", str(tmp_path / "missing.json"))
    assert result.returncode == 2 and "Traceback" not in result.stderr
