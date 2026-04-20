# R01-python — Hello world module + pytest

The smallest possible SOW in Python. One module, one function, one
test. Converge in 1-2 rounds.

## Scope

Create `src/greet/__init__.py` exporting:

```python
def greet(name: str) -> str:
    return f"Hello, {name}!"
```

Create `tests/test_greet.py` with at least one `def test_…():` that
asserts `greet("world")` contains `"world"`.

Create `pyproject.toml` declaring the package with `[project] name =
"greet"`, `requires-python = ">=3.10"`, `[build-system]` using
`setuptools >=68`, and `[tool.pytest.ini_options] testpaths = ["tests"]`.

## Acceptance

- `pyproject.toml`, `src/greet/__init__.py`, `tests/test_greet.py`
  exist.
- `pip install -e .` completes exit 0 in a fresh venv.
- `pytest` exits 0 with the greet test passing.
- No additional modules, no CLI entrypoint, no setup.py shim.

## What NOT to do

- No numpy / pandas / requests / any runtime dep.
- No async.
- No classes — single function only.
- Do not use tox, nox, or hatch.
