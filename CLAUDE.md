## Agent Identity

At session start, if you see `[AgentGuard] No agent identity set`, ask the user:
1. **Role**: developer / reviewer / ops / security / planner
2. **Driver**: human / claude-code / copilot / ci

Then run: `scripts/write-persona.sh <driver> <role>`

## Project

Octi Pulpo is the coordination brain for the Chitin platform — the scheduler that triages issues, emits work contracts, dispatches agents (Copilot, @claude), manages budgets, and maintains the GitHub label state machine.

**Module**: `github.com/chitinhq/octi-pulpo`
**Language**: Go 1.22+
**Depends on**: Redis, GitHub API

## Key Directories

- `cmd/octi-pulpo/` — binary entrypoint
- `internal/brain/` — dispatch loop, leverage analysis (high blast radius, careful)
- `internal/dispatch/` — triage, contracts, label state, GH Actions adapter
- `internal/store/` — sprint store, Redis, budget tracking
- `internal/webhook/` — GitHub webhook handler

## Build

```bash
go build ./...
go test ./...
golangci-lint run
```

## Assembly Line

This repo is the control plane of the Chitin assembly line. Issues dispatched here follow the label state machine:

```
(open) → agent:claimed → agent:working → agent:review → agent:done
                                                │
                                    agent:blocked (escalation)
```

Copilot handles tier:c issues. @claude handles tier:b issues. Humans handle tier:a.
