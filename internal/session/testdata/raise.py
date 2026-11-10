"""Raises an uncaught exception, for exception breakpoint tests."""


def fail(value):
    raise ValueError(f"bad value {value}")


fail(42)
