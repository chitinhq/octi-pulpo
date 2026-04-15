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
