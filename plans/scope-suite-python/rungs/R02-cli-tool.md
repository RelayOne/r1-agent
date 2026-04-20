# R02-python — CLI that deduplicates args

Python CLI that reads argv, preserves first-seen order, drops duplicates,
prints one per line.

## Scope

Package `uniq_args` in `src/uniq_args/`.

`src/uniq_args/__main__.py`:
```python
import sys

def main():
    seen = set()
    for arg in sys.argv[1:]:
        if arg in seen:
            continue
        seen.add(arg)
        print(arg)

if __name__ == "__main__":
    main()
```

`pyproject.toml` with `[project.scripts] uniq-args = "uniq_args.__main__:main"`.

Tests in `tests/test_cli.py` using `subprocess`:
- No args → empty output, exit 0
- `a b a c b` → `a\nb\nc\n`, exit 0

## Acceptance

- `pyproject.toml` declares the script + pytest in test deps.
- `pip install -e ".[test]"` works in a fresh venv.
- `pytest -q` exits 0; both cases pass.

## What NOT to do

- No third-party deps (stdlib only).
- No click/typer/argparse — plain sys.argv.
- No normalization.
