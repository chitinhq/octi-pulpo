# Copilot Coding Instructions — TypeScript Repos

These instructions apply to all Copilot-authored code in this repository.

## Code Style

- Use TypeScript strict mode (`strict: true` in tsconfig.json).
- All code must pass ESLint with zero findings.
- Follow standard TypeScript conventions: camelCase for variables/functions, PascalCase for classes/interfaces.
- Use `interface` for object shapes that can be implemented, `type` for unions, intersections, and aliases.

## Type Safety

- Avoid `any` type. Use `unknown` for truly unknown values, then narrow with type guards.
- Use proper type annotations for function parameters and return values.
- Leverage TypeScript's advanced types: generics, conditional types, mapped types when appropriate.

## Error Handling

- Use try/catch for synchronous errors, async/await with try/catch for asynchronous operations.
- Create custom error classes for domain-specific errors.
- Never swallow errors silently — always handle or propagate them.

## Async/Await

- Prefer async/await over Promise.then() chains for better readability.
- Use `Promise.all()` for parallel operations when order doesn't matter.
- Handle promise rejections with try/catch or .catch().

## Testing

- Use Jest or Vitest for testing.
- Write unit tests for pure functions, integration tests for components/services.
- Mock external dependencies (APIs, databases) in tests.
- Aim for high test coverage, especially for critical paths.

## Project Structure

- Follow the established project structure (e.g., src/, tests/, etc.).
- Group related files by feature, not by type (feature-based organization).
- Use index.ts files for clean exports.

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

1. ESLint passes with zero errors
2. TypeScript compilation succeeds with no errors
3. All tests pass
4. PR title has a type prefix
5. No protected files modified