"""Command-line entry point for the terminal debugger."""

import argparse
import ipaddress
import sys
from pathlib import Path


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="dap-debugger",
        description="Debug Python and Go programs in your terminal using a JSON session config.",
    )
    parser.add_argument("config", type=Path, help="path to a launch or attach JSON configuration")
    parser.add_argument(
        "--check",
        action="store_true",
        help="validate the configuration without starting a debugger",
    )
    args = parser.parse_args(argv)

    from dap_debugger.config import load_config

    try:
        config = load_config(args.config)
    except (OSError, ValueError) as error:
        print(f"dap-debugger: {error}", file=sys.stderr)
        return 2

    if args.check:
        print(f"Valid configuration: {config.name} ({config.adapter} {config.request})")
        return 0

    if config.connect:
        host = str(config.connect["host"])
        try:
            loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback = host.lower() == "localhost"
        if not loopback:
            print(
                "Warning: DAP is unauthenticated and unencrypted. "
                "Use an SSH tunnel or another trusted private transport.",
                file=sys.stderr,
            )

    from dap_debugger.ui import DebuggerApp

    try:
        DebuggerApp(config).run()
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
