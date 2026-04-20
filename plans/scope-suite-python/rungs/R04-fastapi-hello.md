# R04-python — FastAPI hello endpoint

Single-endpoint FastAPI service. Exercises async route handlers,
Pydantic response models, and TestClient.

## Scope

Package `hello_api` in `src/hello_api/`.

`src/hello_api/main.py`:
```python
from fastapi import FastAPI

app = FastAPI()

@app.get("/hello/{name}")
async def hello(name: str) -> dict:
    return {"greeting": f"Hello, {name}!"}
```

Tests in `tests/test_hello.py` using `fastapi.testclient.TestClient`:
- `/hello/World` → 200 with `{"greeting": "Hello, World!"}`.
- `/hello/Alice` → 200 with `{"greeting": "Hello, Alice!"}`.

## Acceptance

- `pyproject.toml` with `fastapi`, `httpx` (for TestClient);
  `pytest` in test deps.
- `pip install -e ".[test]"` works.
- `pytest -q` exits 0; both tests pass.

## What NOT to do

- No database, no auth, no middleware.
- No extra routes beyond `/hello/{name}`.
- No `uvicorn` server in tests — TestClient only.
