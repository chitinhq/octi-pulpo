<p align="center">
  <img src="docs/assets/logo.png" alt="Octi Pulpo" width="360">
</p>

<p align="center">
Swarm coordination layer for autonomous agent fleets.
</p>

<p align="center">
  <a href="https://github.com/AgentGuardHQ/octi-pulpo"><img src="https://img.shields.io/badge/Status-Alpha-orange" alt="Alpha"></a>
  <a href="https://github.com/AgentGuardHQ/octi-pulpo/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
  <a href="https://pkg.go.dev/github.com/AgentGuardHQ/octi-pulpo"><img src="https://img.shields.io/badge/Go-1.18+-00ADD8?logo=go&logoColor=white" alt="Go"></a>
</p>

---

Running multiple AI agents? They step on each other. Duplicate work. Miss handoffs. Waste budget on the wrong model.

**Octi Pulpo** is the coordination brain that turns agent chaos into a managed pipeline. It routes work through standardized stages (Architect → Implement → QA → Review → Release), picks the cheapest capable model for each stage, and scales agent sessions dynamically based on queue depth. One binary, sub-millisecond tool responses, zero runtime dependencies beyond Redis.


## The Eight Arms

| Arm | Capability | What it does |
|-----|-----------|--------------|
| 🧭 | **Model routing** | Route tasks to optimal model by cost, capability, and governance tier |
| 🧠 | **Shared memory** | Vector DB for accumulated knowledge + Redis for hot state |
| 🤝 | **Agent coordination** | Who's working on what — assignment dedup, handoffs, claims |
| 🔄 | **Feedback loops** | Report up (agent → EM → director), get direction down |
| 🔗 | **Dependency resolution** | Agent A needs agent B's output — wait, notify, unblock |
| 📚 | **Learning aggregation** | Collective knowledge across runs — denial patterns, workarounds, solutions |
| 💓 | **Health broadcasting** | Heartbeats, blocks, completions — real-time swarm awareness |
| 📡 | **Priority signaling** | Push a directive, every agent sees it immediately |

## Quick Start

```bash
# Build from source
git clone https://github.com/AgentGuardHQ/octi-pulpo.git
cd octi-pulpo
go build -o octi-pulpo ./cmd/octi-pulpo/

# Run (requires Redis)
OCTI_REDIS_URL=redis://localhost:6379 ./octi-pulpo
```

Add to any agent via MCP config:

```json
{
  "mcpServers": {
    "octi-pulpo": {
      "command": "/path/to/octi-pulpo",
      "env": { "OCTI_REDIS_URL": "redis://localhost:6379" }
    }
  }
}
```

That's it. Your agent now has access to the full coordination toolkit.

## MCP Tools

Agents interact through standard MCP tools:

| Tool | Purpose |
|------|---------|
| `memory_store` | Save a learning, tagged with agent identity + topic |
| `memory_recall` | Semantic search across swarm knowledge |
| `memory_status` | What are other agents working on right now? |
| `coord_claim` | Claim a task (prevents duplicate work) |
| `coord_signal` | Broadcast completion / block / need-help |
| `coord_wait` | Wait for another agent's output |
| `route_recommend` | Get optimal model for a task type + budget |

## Architecture

```
┌─────────────────────────────────────────────┐
│  Agent Swarm                                │
│  Claude Code · Codex · Copilot · Gemini     │
└────────────────┬────────────────────────────┘
                 │ MCP (stdio JSON-RPC)
┌────────────────▼────────────────────────────┐
│  Octi Pulpo                                 │
│  Coordination · Memory · Routing · Signals  │
└────────┬───────────────────┬────────────────┘
         │                   │
   Redis (hot state)   Vector DB (cold knowledge)
         │
┌────────▼────────────────────────────────────┐
│  AgentGuard Kernel (optional)               │
│  Policy enforcement · Telemetry · Invariants│
└─────────────────────────────────────────────┘
```

Octi Pulpo is **independent** — it works with any agent swarm, with or without governance. When paired with [AgentGuard](https://github.com/AgentGuardHQ/agentguard), it gains governance-aware routing and denial pattern learning.

## Part of the Governed Swarm Platform

| Repo | Role |
|------|------|
| [AgentGuard](https://github.com/AgentGuardHQ/agentguard) | Governance — policy enforcement, telemetry, invariants |
| **Octi Pulpo** | **Coordination — pipeline controller, model routing, Slack control plane** |
| [ShellForge](https://github.com/AgentGuardHQ/shellforge) | Orchestration — multi-runtime agent execution |
| [Preflight](https://github.com/AgentGuardHQ/preflight) | Protocol — universal design-before-you-build standard |
| [Extensions](https://github.com/AgentGuardHQ/agentguard-extensions) | Drivers, integrations, policies, example swarms |

## Stack

- **Go** — Single binary, sub-ms tool responses, zero dependencies
- **Redis** — Hot state: claims, locks, heartbeats, pub/sub signals
- **Vector DB** (Qdrant) — Cold knowledge: learnings, patterns, cross-cycle memory
- **MCP** — Stdio JSON-RPC 2.0 interface, works with any MCP-compatible agent

## Configuration

| Env Variable | Default | Description |
|-------------|---------|-------------|
| `OCTI_REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
| `OCTI_QDRANT_URL` | (disabled) | Qdrant URL for vector memory (e.g. `http://localhost:6333`) |
| `OCTI_HTTP_PORT` | `8080` | HTTP server port for `/api/dispatch` and `/api/memory` |
| `OCTI_NAMESPACE` | `octi` | Key prefix for multi-tenant isolation |
| `ANTHROPIC_API_KEY` | — | Required for Anthropic API dispatch |
| `VOYAGE_API_KEY` | — | Required for Voyage AI embeddings (used with Qdrant) |
| `VOYAGE_EMBED_MODEL` | `voyage-3-lite` | Embedding model (`voyage-3-lite` = $0.02/MTok) |
| `AGENTGUARD_AGENT_NAME` | (auto-detected) | Calling agent identity |
| `OCTI_HTTP_URL` | — | Set on agent hosts to enable OctiBridge (CLI hook → memory) |

## Roadmap

- [x] MCP server with stdio transport
- [x] Redis-backed coordination (claims, signals)
- [x] Shared memory store
- [x] Budget-aware dispatch (priority queue + adaptive backoff)
- [x] Model-tier routing (Frontier/Mid/Light/Free)
- [x] Pipeline controller (5-stage kanban with backpressure)
- [x] Dynamic session scaling (queue depth → agent count)
- [x] Slack control plane (dashboard, commands, brief intake, escalation)
- [x] Admission control (intake scoring, concurrency gates, domain locks)
- [x] Vector search for memory recall (Qdrant + Voyage AI embeddings)
- [x] HTTP dispatch endpoint (`/api/dispatch`, `/api/memory`)
- [x] Model cascading (Haiku→Sonnet→Opus by task complexity)
- [x] Request dedup + result caching (Redis, type-aware TTLs)
- [x] Batches API queue (50% discount, async flush)
- [x] Episodic + procedural memory (learned recipes from episode clusters)
- [x] OctiBridge: AgentGuard CLI hooks → Octi Pulpo memory
- [ ] Dependency resolution (Dagu workflow chains)
- [ ] Health broadcasting with circuit breaker integration
- [ ] Multi-box coordination protocol

## License

[Apache 2.0](LICENSE) — Use it, fork it, ship it.
