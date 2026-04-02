# Copilot Instructions — Python (agentguard-analytics)

## Stack

- **Runtime:** Python 3.11+
- **Framework:** FastAPI with Uvicorn
- **ORM/DB:** SQLAlchemy async + Neon Postgres
- **Validation:** Pydantic v2
- **Testing:** pytest + pytest-asyncio + httpx (AsyncClient)
- **Linting:** ruff

## Code Conventions

### Type Hints

- All function signatures MUST include type hints for parameters and return values.
- Use `from __future__ import annotations` at the top of every module.
- Prefer `list[str]` over `List[str]` (use built-in generics, not `typing` aliases).
- Use `X | None` instead of `Optional[X]`.

### Async/Await

- All FastAPI route handlers MUST be `async def`.
- All database operations MUST use async sessions (`AsyncSession`).
- Never use synchronous blocking calls (`time.sleep`, synchronous `requests`, etc.) inside async functions.
- Use `asyncio.gather()` for concurrent I/O operations.

### FastAPI Patterns

- Use dependency injection for database sessions, auth, and config:
  ```python
  async def get_session() -> AsyncGenerator[AsyncSession, None]:
      async with async_session_maker() as session:
          yield session
  ```
- Group routes with `APIRouter` and register via `app.include_router()`.
- Use `status_code` parameter on route decorators, not manual `Response` construction.
- Return Pydantic models from routes; never return raw dicts.

### Pydantic Models

- All request/response bodies MUST be Pydantic `BaseModel` subclasses.
- Use `model_config = ConfigDict(from_attributes=True)` for ORM models.
- Separate schemas: `FooCreate` (input), `FooRead` (output), `FooUpdate` (partial).
- Use `Field()` for validation constraints, descriptions, and examples.

### Error Handling

- Raise `HTTPException` with appropriate status codes.
- Use custom exception handlers for domain-specific errors.
- Never expose stack traces or internal details in API responses.

## Testing

### pytest Conventions

- Test files: `tests/test_<module>.py`.
- Use `pytest.mark.asyncio` for all async tests.
- Use `httpx.AsyncClient` with `app=app` for integration tests:
  ```python
  @pytest.mark.asyncio
  async def test_create_foo():
      async with AsyncClient(app=app, base_url="http://test") as client:
          resp = await client.post("/foo", json={"name": "bar"})
          assert resp.status_code == 201
  ```
- Use fixtures for database setup/teardown.
- Prefer `factory_boy` or simple fixture factories over raw SQL inserts.

### Coverage

- All new routes MUST have at least one happy-path and one error-path test.
- Run: `pytest --cov=app --cov-report=term-missing`.

## Project Structure

```
app/
  main.py           # FastAPI app entry point
  config.py         # Settings via pydantic-settings
  models/           # SQLAlchemy models
  schemas/          # Pydantic request/response models
  routers/          # APIRouter modules
  services/         # Business logic
  deps.py           # Dependency injection providers
tests/
  conftest.py       # Shared fixtures
  test_*.py         # Test modules
```

## Do NOT

- Use `requests` (synchronous) — use `httpx` instead.
- Write raw SQL strings — use SQLAlchemy ORM or Core expressions.
- Put business logic in route handlers — extract to `services/`.
- Use `Any` as a type hint unless absolutely unavoidable.
- Import from `typing` for types available as builtins (`list`, `dict`, `tuple`, `set`).
