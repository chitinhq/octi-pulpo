# Octi Pulpo Pipeline — Workflow Templates

Generic, reusable GitHub Actions workflows for the three-tier assembly line.

## Workflows

| File | Trigger | Purpose |
|------|---------|---------|
| octi-triage.yml | issues: [opened] | Classify issue complexity → tier:c / tier:b-scope / tier:a-groom |
| octi-copilot-dispatch.yml | issues: [labeled] tier:c | Assign Copilot coding agent |
| octi-pr-gate.yml | pull_request: [opened, synchronize, ready_for_review] | Policy checks, CI gate, assign reviewer |
| octi-review-handler.yml | pull_request_review: [submitted] | Merge / fix loop / escalate |
| octi-sweeper.yml | schedule: every 4h | Detect stuck work, escalate |

## Setup

Run `scripts/setup-pipeline.sh <owner/repo>` to install workflows + labels in a target repo.

## Configuration

Workflows read from `.github/octi-config.json` in the target repo:

```json
{
  "tier_b_mode": "claude-api",
  "anthropic_model_triage": "claude-haiku-4-5-20251001",
  "sweeper_interval_hours": 4,
  "auto_merge": true,
  "ag_gateway_telemetry": false
}
```

Set `tier_b_mode` to `"human-team"` for enterprise deployments (Agero).
