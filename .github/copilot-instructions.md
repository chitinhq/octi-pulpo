# Octi Pulpo — Copilot Instructions

> Copilot acts as **Tier C — Execution Workforce** in this repository.
> Implement well-specified issues, open draft PRs, never merge or approve.

## Project Overview

**Octi Pulpo** is the coordination brain for the Chitin platform. It triages issues, emits work contracts, dispatches agents, manages budgets, and maintains the label state machine that drives the governed SDLC.

**Core principle**: Octi Pulpo is the scheduler. GitHub is an execution surface. The label state machine is the durable source of truth.

## Tech Stack

- **Language**: Go 1.22+
- **Dependencies**: Redis (streams, dedup, cooldown), GitHub API
- **Module**: `github.com/chitinhq/octi-pulpo`
- **Service**: runs as `systemd --user` service on jared-box

## Repository Structure

```
cmd/octi-pulpo/       # Main binary entrypoint
internal/
├── brain/            # Dispatch loop, leverage analysis, constraint detection
├── dispatch/         # Triage, contract emission, GitHub Actions adapter, label state
├── store/            # Sprint store, Redis client, budget tracking
├── webhook/          # GitHub webhook handler
└── config/           # Configuration loading
```

## Build & Test

```bash
go build ./...
go test ./...
golangci-lint run
```

## Coding Standards

- Follow Go conventions (`gofmt`, `go vet`)
- Use `internal/` for non-exported packages
- Error handling: always check and wrap errors with context
- Logging: structured logging via `log/slog`
- No global state — pass dependencies via constructors

## Governance Rules

### DENY
- `git push` to main — always use feature branches
- `git force-push` — never rewrite shared history
- Write to `.env`, SSH keys, credentials
- Write or delete `.claude/` files
- Execute `rm -rf` or destructive shell commands
- Modify `agentguard.yaml` without explicit instruction

### ALWAYS
- Create feature branches: `agent/<type>/issue-<N>`
- Run `go build ./... && go test ./...` before creating PRs
- Include governance report in PR body
- Link PRs to issues (`Closes #N`)

## Three-Tier Model

- **Tier A — Architect** (Claude Opus): Sprint planning, architecture, risk
- **Tier B — Senior** (@claude on GitHub): Complex implementation, code review
- **Tier C — Execution** (Copilot): Implement specified issues, open draft PRs

### PR Rules

- **NEVER merge PRs** — only Tier B or humans merge
- **NEVER approve PRs** — post first-pass review comments only
- Max 300 lines changed per PR (soft limit)
- Always open as **draft PR** first
- If ambiguous, label `needs-spec` and stop

## Critical Areas (extra caution)

- `internal/brain/` — dispatch loop logic, high blast radius
- `internal/dispatch/label_state.go` — label state machine, correctness critical
- `internal/dispatch/triage.go` — triage classification, affects all downstream
- `.env` — contains API keys and secrets, never read or write

## Branch Naming

```
agent/feat/issue-<N>
agent/fix/issue-<N>
agent/refactor/issue-<N>
agent/test/issue-<N>
agent/docs/issue-<N>
```

## Autonomy Directive

- **NEVER pause to ask for clarification** — make your best judgment
- If the issue is ambiguous, label it `needs-spec` and stop
- Default to the **safest option** in every ambiguous situation
