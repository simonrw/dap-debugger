"""Long-running target for attach tests."""

import time


def tick(count):
    message = f"tick {count}"
    print(message, flush=True)
    return message


count = 0
while True:
    tick(count)
    count += 1
    time.sleep(0.1)
