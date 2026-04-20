# R03-python — Two-module package with cross-module tests

Python package with two modules where one imports from the other, and
tests exercise both. Exercises package layout, relative imports, test
fixtures.

## Scope

Package `math_lib/` under `src/` with:
- `src/math_lib/__init__.py` re-exporting public API.
- `src/math_lib/core.py`: `def add(a: int, b: int) -> int` and
  `def mul(a: int, b: int) -> int`.
- `src/math_lib/cli.py`: `def run_cli(args: list[str]) -> str` that
  parses two ints from args and returns `f"{add}\n{mul}"`.

Tests:
- `tests/test_core.py` — `test_add`, `test_mul`.
- `tests/test_cli.py` — `test_run_cli_happy` asserts
  `run_cli(["3", "4"]) == "7\n12"`.

## Acceptance

- `pyproject.toml` with package under `[project]` + pytest test dep.
- `pip install -e ".[test]"` works.
- `pytest -q` exits 0; at least three tests pass.

## What NOT to do

- No external deps.
- No subpackages beyond what's described.
- No CLI entrypoint script — `run_cli` is a function called by tests.
