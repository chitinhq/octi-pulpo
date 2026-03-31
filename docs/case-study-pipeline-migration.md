# Case Study: 128-Agent Org Chart to 5-Stage Pipeline

We migrated a 128-agent swarm from a traditional org chart (9 squads x 5 roles) to a 5-stage kanban pipeline with model-tier routing. Same $320/month budget, 75% fewer active sessions, dramatically less waste.

**Full details:** See `docs/superpowers/specs/2026-03-31-pipeline-orchestration-design.md` in the workspace repo.

## Before

- 9 squads, each with PL/Arch/Sr/Jr/QA
- 128 fixed agents, many burning Opus on simple work
- Claude Code budget burned out by Tuesday
- Copilot/Codex/Gemini sitting 80% idle
- ~30% rework from merge conflicts and bad specs

## After

```
Architect (Opus) → Implement (Sonnet) → QA (Free) → Review (Sonnet/Opus) → Release (CI)
```

- 10-20 dynamic sessions, scaled by queue depth
- Opus only for design + high-risk review
- Budget spread across all 5 drivers
- Backpressure prevents queue flooding
- GitHub labels track pipeline stage

## Key Numbers

| Metric | Before | After |
|---|---|---|
| Active agents | 48 | 12 |
| Opus sessions | 20+ | 0-3 |
| Budget runway | Tuesday | Friday |
| Rework | 30% | 10% |

## Built With

- `internal/pipeline/` — stage machine, queue monitor, backpressure, scaler
- `internal/routing/modeltier.go` — Frontier/Mid/Light/Free routing
- `internal/dispatch/pipeline_dispatch.go` — stage→tier→driver
- `internal/dispatch/slack_pipeline.go` — Slack control plane
- `cmd/octi-pipeline/` — controller daemon
