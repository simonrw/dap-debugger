"""Small target for the Python launch configuration."""

import time


def greet(name: str) -> str:
    message = f"Hello, {name}!"
    print(message)
    return message


for index in range(5):
    greeting = greet(f"debugger {index}")
    time.sleep(1)
