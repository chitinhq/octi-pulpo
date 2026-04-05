# Copilot Code Review Instructions — TypeScript Repos

These instructions define how Copilot should review pull requests in TypeScript repositories.

## Review Philosophy

Approve if the code is **correct and safe**, even if style is not perfect.
Copilot PRs prioritize working software — do not block on cosmetic issues.

## Severity: Must-Fix (Request Changes)

These issues must be resolved before approval:

- **Type safety violations** — use of `any` type without justification, missing type annotations on public APIs.
- **Unhandled errors** — promises without `.catch()` or async operations without try/catch.
- **Memory leaks** — event listeners not removed, subscriptions not unsubscribed, resources not cleaned up.
- **Security issues** — hardcoded secrets, SQL injection vulnerabilities, XSS vulnerabilities.
- **Breaking changes to public API** — changes to exported types/functions without proper versioning or documentation.

## Severity: Should-Fix (Comment, Do Not Block)

Leave a comment but do not block approval:

- **Missing tests** — new functionality without corresponding tests.
- **Complex functions** — functions exceeding ~50 lines that could be broken up.
- **Code duplication** — repeated logic that could be extracted.
- **Missing error handling** — operations that could fail but errors are not propagated.

## Severity: Nice-to-Have (Comment Only)

Optional improvements — mention if noticed, never block:

- **Documentation** — missing JSDoc comments on exported functions/classes.
- **Code organization** — files getting too large, could be split.
- **Performance optimizations** — inefficient algorithms or data structures.

## Self-Referential Protection

For the **octi-pulpo** repository specifically:

**NEVER approve changes to the `workflows/` directory.**

This directory contains the pipeline definition files (workflow YAMLs, setup scripts, and these instruction files). Changes to `workflows/` must be reviewed and approved by a human maintainer. Flag any such PR with a comment:

> This PR modifies `workflows/` which contains pipeline definitions. Human review required.

## Approval Criteria

Approve the PR if:

1. No must-fix issues found
2. Tests pass
3. TypeScript compilation succeeds with no errors
4. Code does what the PR description claims
5. No protected files (`.env`, `agentguard.yaml`, `.claude/`) are modified
6. Pipeline labels are not manually altered