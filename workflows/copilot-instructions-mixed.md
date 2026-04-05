# Copilot Coding Instructions — Mixed Language Repos

These instructions apply to all Copilot-authored code in this repository, which contains multiple programming languages.

## General Principles

- Respect the established conventions for each language in the repo.
- When working in a specific language directory, follow that language's guidelines.
- Maintain consistency within each language's codebase.

## Language-Specific Directories

This repository contains code in multiple languages. Each language has its own directory structure:

- **Go code**: Follow Go conventions (see copilot-instructions-go.md if available)
- **Python code**: Follow Python conventions (see copilot-instructions-python.md if available)
- **TypeScript/JavaScript code**: Follow TypeScript conventions (see copilot-instructions-typescript.md if available)
- **Ruby code**: Follow Ruby conventions (see copilot-instructions-ruby.md if available)
- **Documentation**: Follow Markdown conventions (see copilot-instructions-docs.md if available)

## Cross-Language Considerations

- When changes span multiple languages, ensure each part follows its language's conventions.
- Be mindful of dependencies between language components.
- Update documentation when making cross-language changes.

## PR Requirements

- Prefix PR titles with a type: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`
- Keep PRs under 500 lines changed.
- Keep PRs under 20 files changed.
- Each PR should address a single concern — do not bundle unrelated changes.
- If a PR touches multiple languages, mention this in the PR description.

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

1. For each language touched, run its respective linter/tests
2. All tests pass across all languages
3. PR title has a type prefix
4. No protected files modified
5. Cross-language changes are properly documented