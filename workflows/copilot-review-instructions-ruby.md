# Copilot Code Review Instructions — Ruby Repos

These instructions define how Copilot should review pull requests in Ruby repositories.

## Review Philosophy

Approve if the code is **correct and safe**, even if style is not perfect.
Copilot PRs prioritize working software — do not block on cosmetic issues.

## Severity: Must-Fix (Request Changes)

These issues must be resolved before approval:

- **Unhandled exceptions** — code that could raise exceptions without proper rescue.
- **Security vulnerabilities** — hardcoded secrets, SQL injection, command injection.
- **Memory leaks** — objects not properly garbage collected due to references.
- **Breaking changes to public API** — changes to public methods without proper versioning.

## Severity: Should-Fix (Comment, Do Not Block)

Leave a comment but do not block approval:

- **Missing tests** — new functionality without corresponding tests.
- **Complex methods** — methods exceeding ~30 lines that could be broken up.
- **Code duplication** — repeated logic that could be extracted.
- **Missing documentation** — public methods without YARD comments.

## Severity: Nice-to-Have (Comment Only)

Optional improvements — mention if noticed, never block:

- **Code style** — RuboCop offenses that don't affect correctness.
- **Performance** — inefficient algorithms or data structures.
- **Organization** — classes getting too large.

## Self-Referential Protection

For the **octi-pulpo** repository specifically:

**NEVER approve changes to the `workflows/` directory.**

This directory contains the pipeline definition files (workflow YAMLs, setup scripts, and these instruction files). Changes to `workflows/` must be reviewed and approved by a human maintainer. Flag any such PR with a comment:

> This PR modifies `workflows/` which contains pipeline definitions. Human review required.

## Approval Criteria

Approve the PR if:

1. No must-fix issues found
2. Tests pass
3. RuboCop passes (or follows project configuration)
4. Code does what the PR description claims
5. No protected files (`.env`, `agentguard.yaml`, `.claude/`) are modified
6. Pipeline labels are not manually altered