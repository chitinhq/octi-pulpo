<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/octopus_1f419.png" alt="Octi Pulpo" width="120">
</p>

<h1 align="center">Octi Pulpo</h1>

<p align="center"><strong>Eight arms. One brain.</strong><br>
Swarm coordination layer for autonomous agent fleets.</p>

<p align="center">
  <a href="https://github.com/jpleva91/octi-pulpo"><img src="https://img.shields.io/badge/Status-Alpha-orange" alt="Alpha"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
</p>

---

Octi Pulpo is the **shared cognitive layer** for agent swarms. It sits between your agents and provides coordination, shared memory, model routing, and feedback loops. Every agent reads from and writes to the collective intelligence via MCP tools.

## The Eight Arms

| Arm | Capability | What it does |
|-----|-----------|--------------|
| 1 | **Model routing** | Route tasks to optimal model by cost, capability, and governance tier |
| 2 | **Shared memory** | Vector DB for accumulated knowledge + Redis for hot state |
| 3 | **Agent coordination** | Who's working on what — assignment dedup, handoffs, claims |
| 4 | **Feedback loop** | Report up (agent to EM to director), get direction down |
| 5 | **Dependency resolution** | Agent A needs agent B's output — wait, notify, unblock |
| 6 | **Learning aggregation** | Collective knowledge across runs — denial patterns, workarounds, solutions |
| 7 | **Health broadcasting** | Heartbeats, blocks, completions — real-time swarm awareness |
| 8 | **Priority signaling** | Push a directive, every agent sees it immediately |

## Architecture

```
Agent Swarm (Claude Code, Codex, Copilot, Gemini, Goose)
    ↕ MCP tools
Octi Pulpo (coordination brain)
    ↕ Redis (hot state) + Vector DB (cold knowledge)
    ↓ task context
AgentGuard Kernel (governance)  — optional, independent
    ↓ governed execution
LLM Providers
```

## MCP Tools

Agents interact with Octi Pulpo through standard MCP tools — no per-agent changes needed:

```
memory_store     — save a learning, tagged with agent identity + topic
memory_recall    — semantic search across swarm knowledge
memory_status    — what are other agents working on right now?
memory_lessons   — what has the swarm learned about [topic]?
coord_claim      — claim a task (prevents duplicate work)
coord_wait       — wait for another agent's output
coord_signal     — broadcast completion / block / need-help
route_recommend  — get optimal model for a task type
```

## Quick Start

```bash
go build -o octi-pulpo ./cmd/octi-pulpo/
OCTI_REDIS_URL=redis://localhost:6379 ./octi-pulpo
```

Or run via MCP config in your agent's settings:
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

## Stack

- **Go** — Single binary, sub-ms tool responses, zero dependencies
- **Redis** — Hot state: assignments, locks, heartbeats, cycle coordination, pub/sub signals
- **Dagu** — Workflow orchestration for multi-agent dependency chains (Arm 5)
- **Vector DB** (Qdrant) — Cold knowledge: learnings, patterns, cross-cycle memory
- **MCP Server** — Stdio JSON-RPC interface for all agent runtimes

## Relationship to AgentGuard

Octi Pulpo is **independent** — it works with any agent swarm, with or without AgentGuard governance. When paired with AgentGuard, it gains governance-aware routing (model selection respects trust tiers) and denial pattern learning (invariant violations feed the knowledge base automatically).

## License

[Apache 2.0](LICENSE)
