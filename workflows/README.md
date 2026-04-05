# Octi Pulpo Pipeline — Workflow Templates

Generic, reusable GitHub Actions workflows for the three-tier assembly line.

## Architecture Split

**Octi Pulpo (Linux box)** handles all AI/LLM work:
- Issue triage (Claude API via AnthropicAdapter)
- Issue planning/scoping (Claude API)
- Senior coding escalation (ShellForge driver)
- Budget tracking (BudgetStore)
- Labels issues via GitHub API (PAT stored locally)

**GitHub Actions (per-repo)** handles the Copilot lifecycle:
- Copilot dispatch when `tier:c` label appears
- PR policy checks + CI gate
- Review routing (merge / fix loop / escalate)
- Scheduled sweeper for stuck work

No Claude API keys or external secrets needed in GitHub — only the default `GITHUB_TOKEN`.

## Workflows

| File | Trigger | Purpose |
|------|---------|---------|
| octi-copilot-dispatch.yml | issues: [labeled] tier:c | Assign Copilot coding agent |
| octi-pr-gate.yml | pull_request + check_suite | Policy checks, CI gate, assign reviewer |
| octi-review-handler.yml | pull_request_review: [submitted] | Merge / fix loop / escalate |
| octi-sweeper.yml | schedule: every 4h | Detect stuck work, escalate |

## Setup

```bash
# Default (open source)
scripts/setup-pipeline.sh chitinhq/agentguard-cloud

# Custom prefix (enterprise — avoids IP concerns)
scripts/setup-pipeline.sh myorg/frontend --prefix amd

# Preview without changes
scripts/setup-pipeline.sh myorg/frontend --prefix amd --dry-run
```

## Configuration

Workflows read from `.github/<prefix>-config.json` in the target repo:

```json
{
  "tier_b_mode": "claude-api",
  "sweeper_interval_hours": 4,
  "auto_merge": true,
  "ag_gateway_telemetry": false
}
```

Set `tier_b_mode` to `"human-team"` for enterprise deployments where
humans handle Tier B work instead of Claude API.

## Required Secrets

**None in GitHub** for Claude API. All AI/LLM secrets stay on the Octi Pulpo Linux box.

The only token used by GH Actions workflows is `OCTI_PAT` (org-level) for
cross-repo operations. For same-repo operations, the default `GITHUB_TOKEN` works.
