"""Exercise launch and terminal input inside a controlling pseudo-terminal."""

import asyncio
import os
import sys

from dap_debugger.config import load_config
from dap_debugger.session import DebugSession


async def main():
    session = DebugSession(load_config(sys.argv[1]))
    try:
        await session.start()
        foreground = os.tcgetpgrp(sys.stdin.fileno())
        assert foreground == os.getpgrp(), (
            f"Adapter stole terminal foreground: expected {os.getpgrp()}, got {foreground}"
        )
        print("TERMINAL_READY", flush=True)
        received = await asyncio.to_thread(sys.stdin.readline)
        assert received == "keyboard-and-mouse\n"
        print("TERMINAL_INPUT_OK", flush=True)
    finally:
        await session.close()


asyncio.run(main())
