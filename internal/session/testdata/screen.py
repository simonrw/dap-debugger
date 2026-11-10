"""Draws a full-screen display and reads one raw key, for terminal tests."""

import os
import sys
import termios
import tty

fd = sys.stdin.fileno()
saved = termios.tcgetattr(fd)
sys.stdout.write("\x1b[?1049h\x1b[2J\x1b[3;10HFULL SCREEN\x1b[5;1Hpress a key")
sys.stdout.flush()
try:
    tty.setraw(fd)
    key = os.read(fd, 16).decode()
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, saved)
sys.stdout.write("\x1b[?1049l")
print(f"got key {key!r}")
