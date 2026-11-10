"""Starts a child Python process for child-session tests."""

import os
import subprocess
import sys

child = os.path.join(os.path.dirname(__file__), "child.py")
subprocess.run([sys.executable, child], check=True)
print("parent done")
