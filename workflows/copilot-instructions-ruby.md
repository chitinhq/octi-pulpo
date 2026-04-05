# Copilot Coding Instructions — Ruby Repos

These instructions apply to all Copilot-authored code in this repository.

## Code Style

- Follow Ruby community style guide.
- All code must pass RuboCop with zero findings (or follow project-specific RuboCop configuration).
- Use 2-space indentation, not tabs.
- Prefer single quotes for strings without interpolation, double quotes for strings with interpolation.

## Ruby Conventions

- Use snake_case for methods and variables, CamelCase for classes and modules.
- Prefer `attr_reader`, `attr_writer`, `attr_accessor` over manual getter/setter methods.
- Use Ruby's built-in methods and idioms (e.g., `Enumerable` methods).

## Error Handling

- Use exceptions for exceptional conditions, not control flow.
- Rescue specific exceptions, not the generic `Exception` class.
- Use `begin/rescue/ensure` blocks for error handling.

## Testing

- Use RSpec or Minitest as per project configuration.
- Write descriptive test names that explain expected behavior.
- Use factories (FactoryBot) or fixtures for test data.
- Mock external dependencies in tests.

## Project Structure

- Follow standard Ruby project structure: lib/, spec/ or test/, etc.
- Use namespaces (modules) to organize code logically.
- Keep files focused and classes small (Single Responsibility Principle).

## PR Requirements

- Prefix PR titles with a type: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`
- Keep PRs under 500 lines changed.
- Keep PRs under 20 files changed.
- Each PR should address a single concern — do not bundle unrelated changes.

## Protected Files — Do Not Modify

The following paths are off-limits. Do not create, edit, or delete them:

- `.env` and any `.env.*` files
- `agentguard.yaml`
- `.claude/` directory and all contents

## Pipeline Labels

Pipeline labels are managed by the Octi Pulpo pipeline and are **read-only**.
Do not add, remove, or modify these labels manually:

- `tier:c`, `tier:b-scope`, `tier:b-code`, `tier:a`, `tier:a-groom`
- `tier:ci-running`, `tier:review`, `tier:needs-revision`
- `triage:needed`, `needs:human`, `agent:review`

## Pre-Submit Checklist

Before marking a PR as ready:

1. RuboCop passes with zero offenses
2. All tests pass
3. PR title has a type prefix
4. No protected files modified