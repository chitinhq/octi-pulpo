# Dispatcher silent-loss — claim-without-execution

## Summary

The event-driven dispatcher (`internal/dispatch/dispatcher.go`,
`Dispatcher.Dispatch` / `DispatchBudget`) set `result.Action = "dispatched"`
as soon as it had **routed** an event and **enqueued** the task to Redis —
without ever invoking the execution-surface adapter (HTTP
`repository_dispatch`, Anthropic API, Claude Code CLI, etc.) for the
routed driver. Any input the adapter would have rejected (e.g. an event
with empty `Repo`) still came back labeled `"dispatched"`, so observers,
metrics, and the `recordDispatch` audit trail all reported success while
the agent surface saw nothing.

The lie shipped in `5f0dc2f` (event-driven dispatcher, 2026-03-29) and
lived for ~17 days until `5dc4e27` (2026-04-15) gated `Action="dispatched"`
behind an actual `adapter.Dispatch(ctx, task)` call. Class-of-bug:
**claim-without-execution** — a status field flipped to a success literal
before the side effect that justifies it has been attempted.

## BEFORE (silent-loss path) — `5f0dc2f` … `eeef6f2`

```
                  Dispatch(event, agent, priority)
                              │
                              ▼
                ┌──────────────────────────┐
                │ claim distributed lock   │  fail → "claimed_by_other"
                └──────────────┬───────────┘
                               ▼
                ┌──────────────────────────┐
                │ route(event) → driver    │  no match → "unroutable"
                └──────────────┬───────────┘
                               ▼
                ┌──────────────────────────┐
                │ Enqueue(agent, priority) │  Redis ZADD only
                └──────────────┬───────────┘
                               ▼
                ┌──────────────────────────┐
                │ BridgeToFileQueue(agent) │  best-effort, errors swallowed
                └──────────────┬───────────┘
                               ▼
        ╔══════════════════════════════════════════╗
        ║  result.Action  = "dispatched"   ◄── LIE ║
        ║  result.Reason  = "dispatched via …"     ║
        ╚══════════════════════════╤═══════════════╝
                                   ▼
                ┌──────────────────────────┐
                │ recordDispatch(...)      │  audit trail records success
                └──────────────┬───────────┘
                               ▼
                            return

        ░░ MISSING: adapter.Dispatch(ctx, task) ░░
        ░░ no HTTP/CLI/API call ever happens   ░░
```

The gap between *route* and *claim of dispatch* contained zero execution.
A failure mode at the surface (rejected payload, network down, missing
adapter) was indistinguishable on the wire from a healthy dispatch.

## AFTER (adapter-gated) — `5dc4e27`

```
                  Dispatch(event, agent, priority)
                              │
                              ▼
                ┌──────────────────────────┐
                │ claim + route + enqueue  │  (unchanged)
                └──────────────┬───────────┘
                               ▼
                ┌──────────────────────────┐
                │ selectAdapter(driver)    │
                └──────────────┬───────────┘
                               │
        ┌──────────────────────┼──────────────────────────┐
        ▼                      ▼                          ▼
  adapter == nil         adapter == nil            adapter != nil
  AND len(adapters)==0   AND len(adapters)>0
        │                      │                          │
        ▼                      ▼                          ▼
  Action="dispatched"    Action="unroutable"     adapter.Dispatch(ctx,task)
  Reason="queued via …;  Reason="no adapter             │
   no adapter             registered for                │
   registered"            driver q"                     │
   (legacy fallback)                                    │
                                                ┌───────┴────────┐
                                                ▼                ▼
                                            err == nil       err != nil
                                                │                │
                                                ▼                ▼
                                        Action="dispatched"  Action="failed"
                                        Reason="… via HTTP"  Reason=err.Error()
                              ▼
                ┌──────────────────────────┐
                │ recordDispatch(...)      │
                └──────────────┬───────────┘
                               ▼
                            return
```

`Action="dispatched"` now means *an execution surface accepted the task*
(or, on the explicit legacy path, *no surface is registered and the
caller is consuming the Redis queue directly*). The three honest outcomes
— `dispatched | failed | unroutable` — line up with the three real
states of the world.

## Detection heuristic

How to spot this bug class elsewhere in the codebase:

- Any field named `Action` / `Status` / `Result` / `State` assigned a
  success literal (`"dispatched"`, `"sent"`, `"ok"`, `"delivered"`,
  `"committed"`) **before** the network / disk / IPC call that the
  literal is supposed to attest to.
- A success path that `return`s without an error from a function whose
  *only* side effect is enqueueing to a buffer the caller does not
  control end-to-end (Redis, Kafka, a file queue, an in-process channel).
  Enqueue is not delivery.
- Audit / telemetry calls (`recordDispatch`, `emit`, `metrics.Inc`)
  invoked on the same code path that sets the success literal — if both
  fire before the real side effect, your dashboards will lie in lockstep.
- Adapter / driver / handler interfaces that exist in the package but
  are *not referenced* in the function that claims to invoke them. Grep
  for the interface name inside the dispatch path; absence is the smell.
- Tests that assert on `result.Action == "dispatched"` without a spy /
  fake on the surface adapter confirming it was called. Green tests over
  a silent loss.

## Suspects

(Filled by curie — sibling silent-loss patterns in `internal/`.)

## Suspects — pattern mine (curie, 2026-04-15)

Read-only sweep of `internal/` for the claim-without-execution pattern. Scope:
any field named `Action` / `Status` / `Result` / `State` set to a success literal
before the side effect that justifies it has been confirmed.

| file:line | function | suspected success-claim | missing/deferred side-effect | confidence | notes / issue |
|---|---|---|---|---|---|
| `internal/dispatch/claude_code_adapter.go:126` | `ClaudeCodeAdapter.Dispatch` | `result.Status = "completed"` | `git push` + `gh pr create` run AFTER the claim; on failure only `result.Error` is populated, Status is not reverted. Outer dispatcher at `dispatcher.go:264` only downgrades on `Status == "failed"`, so `Action="dispatched"` for a run that never reached origin. | **high** | [chitinhq/octi#241](https://github.com/chitinhq/octi/issues/241) |
| `internal/dispatch/copilot_cli_adapter.go:139` | `CopilotCLIAdapter.Dispatch` | `result.Status = "completed"` | Identical to above — push/PR are best-effort after the Status flip. | **high** | [chitinhq/octi#242](https://github.com/chitinhq/octi/issues/242) |
| `internal/dispatch/clawta_adapter.go:151` | `ClawtaAdapter.Dispatch` | `result.Status = "completed"` | Identical pattern + secondary contamination: episodic learner at line 186 records the (still "completed") outcome on push failure, teaching it that a prompt succeeded when in truth the work was silently dropped. | **high** | [chitinhq/octi#243](https://github.com/chitinhq/octi/issues/243) |
| `internal/sprint/store.go:673` | `Store.markClosedItems` | `item.Status = "done"` + `marked++` | `s.rdb.Set(...)` return value ignored. Sibling `tombstoneFromOpenSet:710` DOES check `.Err()` — asymmetric handling is the tell. On Redis flap, caller logs "marked N closed" while persisted state is stale. | **med** | [chitinhq/octi#244](https://github.com/chitinhq/octi/issues/244) |
| `internal/dispatch/dispatcher.go:237` | `Dispatcher.Dispatch` (legacy path) | `result.Action = "dispatched"` when `len(d.adapters) == 0` | Preserved by the 5dc4e27 fix as an explicit legacy fallback for callers draining the Redis queue directly. Still a claim-without-execution if `adapters` is empty due to *misconfiguration* (as opposed to by-design legacy mode). Reason string marks it, but observers keying on `Action` alone cannot tell. | **low** | flagged only — do NOT touch `dispatcher.go` in this slice; covered by workspace#408. |
| `internal/dispatch/openclaw_adapter.go:118` | `OpenClawAdapter.Dispatch` | `result.Status = "completed"` | Set AFTER `sendMessage` + `waitForResponse` both return without error. Response is from a Matrix bot — "completed" means "bot replied", not "work shipped". Out-of-scope for this class (it's an API-confirmed reply), but worth knowing for any future strengthening of the contract. | **low** | informational only. |
| `internal/dispatch/anthropic_adapter.go:106`, `deepseek_adapter.go:117`, `copilot_adapter.go:155` | adapter `Dispatch` | `result.Status = "completed"` | All three set Status AFTER their HTTP/subprocess call succeeds and the response is decoded/validated. No missing side effect. | — | clean, listed for completeness. |

**Totals:** 3 high, 1 med, 2 low, 3 cleared. 4 issues filed (#241–#244).

**Headline finding:** the three CLI-agent adapters (claude_code, copilot_cli,
clawta) all carry the same shape of bug the dispatcher just fixed: `Status =
"completed"` is set on CLI exit 0, and the follow-up `git push` / `gh pr
create` are best-effort. When push fails, `Status` stays `"completed"`, the
outer dispatcher maps it to `Action="dispatched"`, and the branch is deleted
by `cleanupWorktree` on return — a textbook silent-loss. Same class, three
more surfaces.

