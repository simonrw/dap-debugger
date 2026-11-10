"""Reads input through a terminal for terminal tests."""

import getpass
import sys

name = input("name? ")
print(f"hello {name}")
secret = getpass.getpass("secret? ")
print(f"secret has {len(secret)} characters, tty: {sys.stdin.isatty()}")
